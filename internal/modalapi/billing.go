package modalapi

import (
	"context"
	"errors"
	"io"
	"strconv"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// What Modal's API says about money. It reports what a workspace has spent
// and its rates, but not its credit balance or its spend limit: those are
// only on the dashboard's Usage & Billing page.

// Spend is a workspace's cost in one billing cycle, in US dollars.
type Spend struct {
	Start, End time.Time
	// Metered is the cost before adjustments; Billed is what's invoiced
	// after credits, free storage and other adjustments.
	Metered, Billed float64
	Adjustments     map[string]float64
}

// MonthStart is the first moment of t's month in UTC, where Modal's billing
// cycles begin.
func MonthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// SpendThisMonth returns the workspace's spend in the current billing cycle.
func (c *Client) SpendThisMonth(ctx context.Context) (Spend, error) {
	start := MonthStart(time.Now())
	r, err := withRetry(ctx, func(ctx context.Context) (*pb.WorkspaceBillingSummaryResponse, error) {
		return c.Stub.WorkspaceBillingSummary(ctx, pb.WorkspaceBillingSummaryRequest_builder{StartTimestamp: timestamppb.New(start)}.Build())
	})
	if err != nil {
		return Spend{}, err
	}
	s := Spend{Start: r.GetStartTimestamp().AsTime(), End: r.GetEndTimestamp().AsTime(), Adjustments: map[string]float64{}}
	if s.Metered, err = decimal(r.GetMeteredCost()); err != nil {
		return Spend{}, err
	}
	if s.Billed, err = decimal(r.GetBilledCost()); err != nil {
		return Spend{}, err
	}
	for k, v := range r.GetAdjustments() {
		if f, err := decimal(v); err == nil {
			s.Adjustments[k] = f
		}
	}
	return s, nil
}

// AppCost adds up what the apps cost from start's day until now, from the
// daily billing report, which can lag behind by an hour or so.
func (c *Client) AppCost(ctx context.Context, start time.Time, appIDs ...string) (float64, error) {
	if len(appIDs) == 0 {
		return 0, errors.New("no app")
	}
	stream, err := c.Stub.WorkspaceBillingReport(ctx, pb.WorkspaceBillingReportRequest_builder{
		StartTimestamp: timestamppb.New(start.UTC().Truncate(24 * time.Hour)),
		// The end of today: the report leaves out a partial last interval.
		EndTimestamp: timestamppb.New(time.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)),
		Resolution:   "d",
		AppIds:       appIDs,
	}.Build())
	if err != nil {
		return 0, err
	}
	want := map[string]bool{}
	for _, id := range appIDs {
		want[id] = true
	}
	var sum float64
	for {
		item, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return sum, nil
		}
		if err != nil {
			return 0, err
		}
		if !want[item.GetObjectId()] {
			continue // the filter is a request; don't count anything else
		}
		f, err := decimal(item.GetCost())
		if err != nil {
			return 0, err
		}
		sum += f
	}
}

// Rates returns the workspace's billing rates, in US dollars per unit:
// "cpu" per core-second and "memory" per GiB-second among them (the keys are
// Modal's, and may change).
func (c *Client) Rates(ctx context.Context) (map[string]float64, error) {
	r, err := withRetry(ctx, func(ctx context.Context) (*pb.WorkspaceBillingRatesResponse, error) {
		return c.Stub.WorkspaceBillingRates(ctx, pb.WorkspaceBillingRatesRequest_builder{}.Build())
	})
	if err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for k, v := range r.GetRates() {
		if f, err := decimal(v); err == nil {
			out[k] = f
		}
	}
	return out, nil
}

// DashboardURL returns the address of the workspace's page on modal.com.
func (c *Client) DashboardURL(ctx context.Context) (string, error) {
	r, err := withRetry(ctx, func(ctx context.Context) (*pb.WorkspaceDashboardUrlResponse, error) {
		return c.Stub.WorkspaceDashboardUrlGet(ctx, pb.WorkspaceDashboardUrlRequest_builder{EnvironmentName: c.cfg.Environment}.Build())
	})
	if err != nil {
		return "", err
	}
	return r.GetUrl(), nil
}

// decimal reads one of Modal's decimal strings ("" is zero).
func decimal(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}

// ImageBuildResult asks Modal how an image build ended and why: the Go SDK
// keeps only "terminated due to external shut-down" of a build Modal
// stopped, while Modal may have said more. It returns the build's status
// (e.g. "GENERIC_STATUS_TERMINATED") and Modal's exception text, if any.
func (c *Client) ImageBuildResult(ctx context.Context, imageID string) (status, exception string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stream, err := c.Stub.ImageJoinStreaming(ctx, pb.ImageJoinStreamingRequest_builder{ImageId: imageID, Timeout: 5}.Build())
	if err != nil {
		return "", "", err
	}
	for {
		item, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return "", "", nil
		}
		if err != nil {
			return "", "", err
		}
		if r := item.GetResult(); r != nil && r.GetStatus() != pb.GenericResult_GENERIC_STATUS_UNSPECIFIED {
			return r.GetStatus().String(), r.GetException(), nil
		}
		if item.GetEof() {
			return "", "", nil
		}
	}
}

// AppIDs returns the IDs of the environment's apps with any of these names,
// stopped ones included: an app redeployed or stopped this month still
// counts toward its cost.
func (c *Client) AppIDs(ctx context.Context, names ...string) ([]string, error) {
	r, err := withRetry(ctx, func(ctx context.Context) (*pb.AppListResponse, error) {
		return c.Stub.AppList(ctx, pb.AppListRequest_builder{EnvironmentName: c.cfg.Environment}.Build())
	})
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []string
	for _, a := range r.GetApps() {
		if want[a.GetName()] || want[a.GetDescription()] {
			out = append(out, a.GetAppId())
		}
	}
	return out, nil
}
