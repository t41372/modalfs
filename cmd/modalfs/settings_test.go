package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

// Each setting is changed through the API, as the app changes it.
func TestSettingsChangesOne(t *testing.T) {
	sent := make(chan map[string]any, 1)
	serveCLI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" && r.URL.Path == "/api/settings" {
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			sent <- in
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	for _, c := range []struct {
		args []string
		want map[string]any
	}{
		{[]string{"cache-size", "20"}, map[string]any{"cache_size_gb": float64(20)}},
		{[]string{"cache-size", "5GB"}, map[string]any{"cache_size_gb": float64(5)}},
		{[]string{"read-ahead", "8"}, map[string]any{"read_ahead": float64(8)}},
		{[]string{"mount-root", "~/Volumes"}, map[string]any{"mount_root": "~/Volumes"}},
		{[]string{"transport", "http1"}, map[string]any{"transport": "http1"}},
		{[]string{"download-size", "large"}, map[string]any{"download_size": "large"}},
		{[]string{"download-stall", "30"}, map[string]any{"download_stall_minutes": float64(30)}},
	} {
		cmdSettings(c.args)
		select {
		case got := <-sent:
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("settings %v sent %v, want %v", c.args, got, c.want)
			}
		default:
			t.Errorf("settings %v sent nothing", c.args)
		}
	}
}
