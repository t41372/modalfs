package main

// The menu bar icon's menu and the app menu, in the app's language. The
// daemon's state is polled every two seconds; the menu is rebuilt only when
// what it shows changes.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// trayState is the part of the daemon's /api/state the menus use.
type trayState struct {
	Settings struct {
		Language string `json:"language"`
	} `json:"settings"`
	Mounts []struct {
		ID         string `json:"id"`
		VolumeName string `json:"volume_name"`
		Label      string `json:"label"`
		State      string `json:"state"`
		Status     *struct {
			PendingFiles int   `json:"pending_files"`
			PendingBytes int64 `json:"pending_bytes"`
		} `json:"status"`
		OpenConflicts int    `json:"open_conflicts"`
		UnmountError  string `json:"unmount_error"`
	} `json:"mounts"`
	Downloads []struct {
		State string `json:"state"`
	} `json:"downloads"`
}

// trayModel is what native_darwin.m draws.
type trayModel struct {
	Icon      string    `json:"icon"` // menubar, menubar-up or menubar-attn
	Summary   string    `json:"summary"`
	Volumes   []trayVol `json:"volumes"`
	Downloads string    `json:"downloads,omitempty"`
	Open      string    `json:"open"`
	Settings  string    `json:"settings"`
	Quit      string    `json:"quit"`
}

type trayVol struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Symbol string `json:"symbol"` // an SF Symbol
	Action string `json:"action"` // reveal, mount, volume, or "" (nothing to do yet)
}

// Remote downloads that are using a container on Modal right now.
var downloadRunning = map[string]bool{"starting": true, "running": true, "retrying": true, "finishing": true, "interrupted": true}

// buildTray turns the daemon's state into the menu. st is nil while the
// daemon isn't answering.
func buildTray(st *trayState, s strs) trayModel {
	m := trayModel{Icon: "menubar", Open: s.Open, Settings: s.Settings, Quit: s.Quit}
	if st == nil {
		m.Summary = s.NotRunning
		return m
	}
	var on, problems int
	var pending int64
	var moving bool
	for _, mt := range st.Mounts {
		v := trayVol{ID: mt.ID, Name: mt.VolumeName}
		if mt.Label != "" {
			v.Name = mt.Label
		}
		live := mt.State == "mounted" || mt.State == "starting" || mt.State == "stopping"
		if live {
			on++
		}
		var p int64
		var files int
		if mt.Status != nil {
			p, files = mt.Status.PendingBytes, mt.Status.PendingFiles
		}
		if live {
			pending += p
		}
		switch {
		case mt.State == "error":
			problems++
			v.Detail, v.Symbol, v.Action = s.VolError, "exclamationmark.triangle", "volume"
		case mt.State == "starting":
			moving = true
			v.Detail, v.Symbol = s.VolStarting, "arrow.triangle.2.circlepath"
		case mt.State == "stopping":
			moving = true
			v.Detail, v.Symbol = s.VolStopping, "arrow.up.circle"
		case mt.State != "mounted":
			v.Detail, v.Symbol, v.Action = s.VolOff, "minus.circle", "mount"
		case mt.OpenConflicts > 0:
			problems++
			v.Detail, v.Symbol, v.Action = plural(mt.OpenConflicts, s.VolConflictOne, s.VolConflicts), "exclamationmark.triangle", "volume"
		case mt.UnmountError != "":
			v.Detail, v.Symbol, v.Action = s.VolBusy, "exclamationmark.triangle", "volume"
		case files > 0:
			v.Detail, v.Symbol, v.Action = fmt.Sprintf(s.VolUploading, size(p)), "arrow.up.circle", "reveal"
		default:
			v.Detail, v.Symbol, v.Action = s.VolSynced, "checkmark.circle", "reveal"
		}
		m.Volumes = append(m.Volumes, v)
	}
	switch {
	case problems > 0:
		m.Icon, m.Summary = "menubar-attn", plural(problems, s.NeedLookOne, s.NeedLook)
	case pending > 0 || moving:
		m.Icon = "menubar-up"
		m.Summary = s.Moving
		if pending > 0 {
			m.Summary = fmt.Sprintf(s.Uploading, size(pending))
		}
	case on > 0:
		m.Summary = fmt.Sprintf(s.Mounted, on)
	default:
		m.Summary = s.NothingMounted
	}
	running := 0
	for _, d := range st.Downloads {
		if downloadRunning[d.State] {
			running++
		}
	}
	if running > 0 {
		m.Downloads = plural(running, s.DownloadsOne, s.Downloads)
	}
	return m
}

// plural picks the singular form for 1 (the Chinese words are the same).
func plural(n int, one, other string) string {
	if n == 1 {
		return fmt.Sprintf(one, n)
	}
	return fmt.Sprintf(other, n)
}

// size is the UI's byte format (lib/format.ts): 1024-based, one decimal.
func size(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if f >= 100 || i == 0 {
		return fmt.Sprintf("%.0f %s", f, units[i])
	}
	return strings.Replace(fmt.Sprintf("%.1f %s", f, units[i]), ".0 ", " ", 1)
}

// trayLoop keeps the menu bar icon and the app menu up to date until the
// app quits.
func (a *App) trayLoop(ctx context.Context) {
	hc := &http.Client{Timeout: 3 * time.Second, Transport: a.client.Transport()}
	var lastTray, lastLang string
	for {
		var st *trayState
		if resp, err := hc.Get("http://modalfsd/api/state"); err == nil {
			var s trayState
			if resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&s) == nil {
				st = &s
			}
			resp.Body.Close()
		}
		setting := ""
		if st != nil {
			setting = st.Settings.Language
		}
		lang := resolveLang(setting, nativePreferredLanguage())
		if lang != lastLang {
			lastLang = lang
			raw, _ := json.Marshal(menuLabels[lang])
			nativeSetMenus(raw)
		}
		raw, _ := json.Marshal(buildTray(st, trayStrings[lang]))
		if string(raw) != lastTray && !a.quitting.Load() {
			lastTray = string(raw)
			nativeSetTray(raw)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// resolveLang is web/src/i18n's resolveLanguage: the setting, else the
// system's language (Chinese by its script; Traditional for TW, HK and MO).
func resolveLang(setting, system string) string {
	switch setting {
	case "en", "zh-Hant", "zh-Hans":
		return setting
	}
	l := strings.ToLower(system)
	if !strings.HasPrefix(l, "zh") {
		return "en"
	}
	for _, t := range []string{"hant", "-tw", "-hk", "-mo"} {
		if strings.Contains(l, t) {
			return "zh-Hant"
		}
	}
	return "zh-Hans"
}

// strs are the menu bar menu's words in one language.
type strs struct {
	NotRunning, NothingMounted, Mounted, Uploading, Moving, NeedLook, NeedLookOne, Downloads, DownloadsOne     string
	VolSynced, VolUploading, VolConflicts, VolConflictOne, VolBusy, VolError, VolStarting, VolStopping, VolOff string
	Open, Settings, Quit                                                                                       string
}

var trayStrings = map[string]strs{
	"en": {
		NotRunning: "modalfs isn't running", NothingMounted: "Nothing mounted", Mounted: "%d mounted · all on Modal",
		Uploading: "Uploading %s", Moving: "Mounting or unmounting…", NeedLook: "%d need a look", NeedLookOne: "%d needs a look", Downloads: "%d remote downloads running", DownloadsOne: "%d remote download running",
		VolSynced: "On Modal", VolUploading: "%s to upload", VolConflicts: "%d conflicts", VolConflictOne: "%d conflict", VolBusy: "In use, couldn't unmount",
		VolError: "Couldn't mount", VolStarting: "Mounting…", VolStopping: "Unmounting…", VolOff: "Not mounted · Mount",
		Open: "Open modalfs", Settings: "Settings…", Quit: "Quit modalfs",
	},
	"zh-Hant": {
		NotRunning: "modalfs 沒有在執行", NothingMounted: "沒有掛載任何 Volume", Mounted: "%d 個已掛載 · 全部已同步",
		Uploading: "正在上傳 %s", Moving: "正在掛載或卸載…", NeedLook: "%d 個需要處理", NeedLookOne: "%d 個需要處理", Downloads: "%d 個遠程下載進行中", DownloadsOne: "%d 個遠程下載進行中",
		VolSynced: "已同步", VolUploading: "待上傳 %s", VolConflicts: "%d 個衝突", VolConflictOne: "%d 個衝突", VolBusy: "使用中，無法卸載",
		VolError: "無法掛載", VolStarting: "正在掛載…", VolStopping: "正在卸載…", VolOff: "未掛載 · 掛載",
		Open: "打開 modalfs", Settings: "設定…", Quit: "結束 modalfs",
	},
	"zh-Hans": {
		NotRunning: "modalfs 没有在运行", NothingMounted: "没有挂载任何 Volume", Mounted: "%d 个已挂载 · 全部已同步",
		Uploading: "正在上传 %s", Moving: "正在挂载或卸载…", NeedLook: "%d 个需要处理", NeedLookOne: "%d 个需要处理", Downloads: "%d 个远程下载进行中", DownloadsOne: "%d 个远程下载进行中",
		VolSynced: "已同步", VolUploading: "待上传 %s", VolConflicts: "%d 个冲突", VolConflictOne: "%d 个冲突", VolBusy: "使用中，无法卸载",
		VolError: "无法挂载", VolStarting: "正在挂载…", VolStopping: "正在卸载…", VolOff: "未挂载 · 挂载",
		Open: "打开 modalfs", Settings: "设置…", Quit: "退出 modalfs",
	},
}

// menuLabels are the app menu's words (native_darwin.m nativeSetMenus).
var menuLabels = map[string]map[string]string{
	"en": {
		"about": "About modalfs", "settings": "Settings…", "hide": "Hide modalfs", "hideOthers": "Hide Others", "showAll": "Show All", "quit": "Quit modalfs",
		"edit": "Edit", "undo": "Undo", "redo": "Redo", "cut": "Cut", "copy": "Copy", "paste": "Paste", "selectAll": "Select All",
		"window": "Window", "minimize": "Minimize", "zoom": "Zoom", "close": "Close Window",
	},
	"zh-Hant": {
		"about": "關於 modalfs", "settings": "設定…", "hide": "隱藏 modalfs", "hideOthers": "隱藏其他", "showAll": "顯示全部", "quit": "結束 modalfs",
		"edit": "編輯", "undo": "還原", "redo": "重做", "cut": "剪下", "copy": "拷貝", "paste": "貼上", "selectAll": "全選",
		"window": "視窗", "minimize": "縮到最小", "zoom": "縮放", "close": "關閉視窗",
	},
	"zh-Hans": {
		"about": "关于 modalfs", "settings": "设置…", "hide": "隐藏 modalfs", "hideOthers": "隐藏其他", "showAll": "全部显示", "quit": "退出 modalfs",
		"edit": "编辑", "undo": "撤销", "redo": "重做", "cut": "剪切", "copy": "拷贝", "paste": "粘贴", "selectAll": "全选",
		"window": "窗口", "minimize": "最小化", "zoom": "缩放", "close": "关闭窗口",
	},
}
