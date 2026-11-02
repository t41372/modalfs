package modalfake

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A receipt acknowledges a stored block. A failed write must not create a
// receipt, and retrying after the filesystem is repaired must store the data.
func TestPutBlockStorageFailures(t *testing.T) {
	for _, failure := range []string{"parent is a file", "destination is a directory"} {
		t.Run(failure, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "blocks")
			s := &Server{dir: root, up: newLimiter(0), down: newLimiter(0)}
			data := []byte("only acknowledge data that was stored")
			hash := sha256.Sum256(data)
			key := hex.EncodeToString(hash[:])
			destination := s.blockPath(hash[:])
			switch failure {
			case "parent is a file":
				if err := os.WriteFile(root, []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			case "destination is a directory":
				if err := os.MkdirAll(destination, 0700); err != nil {
					t.Fatal(err)
				}
			}
			put := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPut, "/put/"+key, bytes.NewReader(data))
				req.SetPathValue("hash", key)
				response := httptest.NewRecorder()
				s.putBlock(response, req)
				return response
			}
			response := put()
			if response.Code != http.StatusInternalServerError || strings.HasPrefix(response.Body.String(), "receipt-") {
				t.Fatalf("storage failure was acknowledged: status=%d body=%q", response.Code, response.Body.String())
			}
			if len(s.receipts) != 0 {
				t.Fatal("failed write recorded a receipt")
			}
			if failure == "parent is a file" {
				if err := os.Remove(root); err != nil {
					t.Fatal(err)
				}
			} else {
				entries, err := os.ReadDir(filepath.Dir(destination))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), "put-") {
						t.Fatalf("temporary upload left behind: %s", entry.Name())
					}
				}
				if err := os.Remove(destination); err != nil {
					t.Fatal(err)
				}
			}
			response = put()
			receipt := response.Body.String()
			if response.Code != http.StatusOK || !strings.HasPrefix(receipt, "receipt-") || !s.receipts[key][receipt] {
				t.Fatalf("retry did not acknowledge stored data: status=%d receipt=%q", response.Code, receipt)
			}
			got, err := os.ReadFile(destination)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("stored data: %q, %v", got, err)
			}
			entries, err := os.ReadDir(filepath.Dir(destination))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != filepath.Base(destination) {
				t.Fatalf("unexpected block-store entries: %v", entries)
			}
		})
	}
}
