package downloads

import "strings"

// Why a download failed, when Modal is the reason. Modal's API reports
// neither a workspace's credit nor its spend limit, and its docs say only
// that at the limit it "stops workloads that would incur additional
// out-of-pocket charges", not what a stopped or refused container reports.
// So a message that names the cause is believed, and one where Modal stopped
// the work without a reason is shown as such, with the spend limit as one
// possible cause rather than the cause.
const (
	// WhyBilling: Modal's message says the workspace is out of credit or
	// over a spend limit or budget.
	WhyBilling = "billing"
	// WhyUnexplained: Modal stopped or refused the work without saying why
	// (an image build "terminated due to external shut-down", a container
	// gone without the agent's last word). A spend limit is one cause.
	WhyUnexplained = "unexplained"
)

var billingWords = []string{
	"spend limit", "spending limit", "budget", "credit", "billing", "payment",
	"usage limit", "insufficient funds", "out of funds",
}

var unexplainedWords = []string{
	"terminated due to external shut-down",
	"function call was cancelled by user or a failure",
}

// Why classifies a failure message from Modal: WhyBilling, WhyUnexplained
// or "".
func Why(msg string) string {
	m := strings.ToLower(msg)
	for _, w := range billingWords {
		if strings.Contains(m, w) {
			return WhyBilling
		}
	}
	for _, w := range unexplainedWords {
		if strings.Contains(m, w) {
			return WhyUnexplained
		}
	}
	return ""
}

// ImageIDIn returns the image ID in an image build error ("Image build for
// im-… terminated …"), or "".
func ImageIDIn(msg string) string {
	i := strings.Index(msg, "Image build for im-")
	if i < 0 {
		return ""
	}
	id := msg[i+len("Image build for "):]
	if j := strings.IndexFunc(id, func(r rune) bool {
		return !(r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}); j >= 0 {
		id = id[:j]
	}
	return id
}
