package daemon

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/t41372/modalfs/internal/downloads"
)

// What the Downloads page says about money. Modal's API tells what a
// workspace has spent this month and its rates, but not its credit or its
// spend limit, so that's what's shown: spending, labelled as such, and a
// link to the dashboard page where the limit and the credit are.

// Billing is an account's spending this month, as the API returns it.
type Billing struct {
	PeriodStart int64 `json:"period_start"`
	PeriodEnd   int64 `json:"period_end"`
	// Metered is this month's cost before credits and other adjustments;
	// Billed is what Modal will charge after them.
	Metered     float64            `json:"metered"`
	Billed      float64            `json:"billed"`
	Adjustments map[string]float64 `json:"adjustments,omitempty"`
	// Downloads is what remote downloads cost this month in this
	// environment (the downloads app and its deploy Sandboxes), from
	// Modal's daily report, which lags by an hour or so. Absent if
	// unknown.
	Downloads      *float64 `json:"downloads,omitempty"`
	DownloadsError string   `json:"downloads_error,omitempty"`
	// Hourly is what a download costs an hour while it runs, per size, at
	// the workspace's rates. Absent if the rates couldn't be read.
	Hourly map[string]float64 `json:"hourly,omitempty"`
	// UsageURL is the dashboard's Usage & Billing page, where the spend
	// limit and the credit are.
	UsageURL string `json:"usage_url"`
	At       int64  `json:"at"` // when this was read
}

// billingTTL is how long a reading is reused: the report behind it moves
// hourly, and the page may ask often.
const billingTTL = 5 * time.Minute

type billingCache struct {
	mu sync.Mutex
	m  map[string]Billing
}

func (m *dlManager) billing(ctx context.Context, accountID, env string, fresh bool) (Billing, error) {
	key := accountID + "\x00" + env
	m.bills.mu.Lock()
	b, ok := m.bills.m[key]
	m.bills.mu.Unlock()
	if ok && !fresh && time.Since(time.Unix(b.At, 0)) < billingTTL {
		return b, nil
	}
	b, err := m.billingFor(ctx, accountID, env)
	if err != nil {
		return b, err
	}
	b.At = time.Now().Unix()
	m.bills.mu.Lock()
	if m.bills.m == nil {
		m.bills.m = map[string]Billing{}
	}
	m.bills.m[key] = b
	m.bills.mu.Unlock()
	return b, nil
}

// readBilling asks Modal. Only the month's spend is needed; the rest is
// filled in when it can be.
func (m *dlManager) readBilling(ctx context.Context, accountID, env string) (Billing, error) {
	c, _, err := m.d.clientFor(accountID, env)
	if err != nil {
		return Billing{}, err
	}
	s, err := c.SpendThisMonth(ctx)
	if err != nil {
		return Billing{}, friendly(err)
	}
	b := Billing{PeriodStart: s.Start.Unix(), PeriodEnd: s.End.Unix(), Metered: s.Metered, Billed: s.Billed, Adjustments: s.Adjustments}
	if ids, err := c.AppIDs(ctx, downloads.AppName, downloads.AppName+"-deploy"); err != nil {
		b.DownloadsError = friendly(err).Error()
	} else if len(ids) == 0 {
		zero := 0.0
		b.Downloads = &zero
	} else if cost, err := c.AppCost(ctx, s.Start, ids...); err != nil {
		b.DownloadsError = friendly(err).Error()
	} else {
		b.Downloads = &cost
	}
	if rates, err := c.Rates(ctx); err == nil {
		b.Hourly = hourly(rates)
	}
	b.UsageURL = "https://modal.com/settings/usage"
	if u, err := c.DashboardURL(ctx); err == nil {
		b.UsageURL = usageURL(u)
	}
	return b, nil
}

// hourly works out what each download size costs an hour from the rates
// for a Function's cores and memory, or nil if they aren't there.
func hourly(rates map[string]float64) map[string]float64 {
	cpu, okC := rates["cpu_hour_cost"]
	mem, okM := rates["mem_gib_hour_cost"]
	if !okC || !okM {
		return nil
	}
	out := map[string]float64{}
	for name, s := range downloads.Sizes {
		out[name] = s.CPU*cpu + float64(s.MemoryMiB)/1024*mem
	}
	return out
}

// usageURL turns the workspace's dashboard address
// (https://modal.com/apps/WORKSPACE/ENV) into its Usage & Billing page.
func usageURL(dashboard string) string {
	u, err := url.Parse(dashboard)
	if err == nil {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 && parts[0] == "apps" && parts[1] != "" {
			u.Path, u.RawQuery = "/settings/"+parts[1]+"/usage", ""
			return u.String()
		}
	}
	return "https://modal.com/settings/usage"
}

func (d *Daemon) billingRoutes(mux *http.ServeMux) {
	// ?env= ; ?fresh=1 skips the few minutes' cache.
	mux.HandleFunc("GET /api/accounts/{id}/billing", func(w http.ResponseWriter, r *http.Request) {
		if d.dl == nil {
			writeErr(w, errNoDownloads)
			return
		}
		b, err := d.dl.billing(r.Context(), r.PathValue("id"), r.URL.Query().Get("env"), r.URL.Query().Get("fresh") == "1")
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, b)
	})
}
