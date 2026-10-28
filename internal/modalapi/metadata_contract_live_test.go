//go:build live

package modalapi

// What Modal's Volume API does with symlinks, hard links, permissions and
// modification times that a container made (docs/design/fs-metadata.md).
// modalfs's design rests on these limits; a failure here means Modal changed
// and the design should be revisited. Each container observation uses a fresh
// Sandbox, after the writer synced the volume and exited.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modal "github.com/modal-labs/modal-client/go"
	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

type metadataProbe struct {
	t     *testing.T
	c     *Client
	sdk   *modal.Client
	app   *modal.App
	vol   string
	steps []map[string]any
}

func (p *metadataProbe) record(op string, result any, err error) {
	s := map[string]any{"operation": op, "result": result}
	if err != nil {
		s["error"] = err.Error()
	}
	p.steps = append(p.steps, s)
	b, _ := json.Marshal(s)
	p.t.Log(string(b))
}

func (p *metadataProbe) container(script string) string {
	p.t.Helper()
	ctx := context.Background()
	sb, err := p.sdk.Sandboxes.Create(ctx, p.app, p.sdk.Images.FromRegistry("python:3.12-slim", nil), &modal.SandboxCreateParams{
		Volumes: map[string]*modal.Volume{"/vol": {VolumeID: p.vol}}, Timeout: 3 * time.Minute, CPU: 0.25, MemoryMiB: 512,
	})
	if err != nil {
		p.t.Fatal(err)
	}
	terminated := false
	stop := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		code, err := sb.Terminate(cleanupCtx, &modal.SandboxTerminateParams{Wait: true})
		p.record("terminate Sandbox "+sb.SandboxID, code, err)
		return err
	}
	defer func() {
		if !terminated {
			err := stop()
			if err != nil {
				p.t.Errorf("terminate %s: %v", sb.SandboxID, err)
			}
		}
	}()
	script += "\nimport subprocess; subprocess.run(['sync','/vol'],check=True)\n"
	proc, err := sb.Exec(ctx, []string{"python3", "-c", script}, nil)
	if err != nil {
		p.t.Fatal(err)
	}
	out, err := io.ReadAll(proc.Stdout)
	if err != nil {
		p.t.Fatal(err)
	}
	stderr, err := io.ReadAll(proc.Stderr)
	if err != nil {
		p.t.Fatal(err)
	}
	code, err := proc.Wait(ctx, nil)
	p.record("container python3 -c "+script, map[string]any{"stdout": string(out), "stderr": string(stderr), "exit": code, "sandbox": sb.SandboxID}, err)
	if err != nil || code != 0 {
		p.t.Fatalf("container exit %d: %v %s", code, err, stderr)
	}
	err = stop()
	if err != nil {
		p.t.Fatal(err)
	}
	terminated = true
	return string(out)
}

const metadataStat = `import os,stat,json
rows=[]
for root,dirs,files in os.walk('/vol',followlinks=False):
 for name in sorted(dirs+files):
  path=os.path.join(root,name); s=os.lstat(path)
  row=dict(path=os.path.relpath(path,'/vol'),mode=oct(s.st_mode),ino=s.st_ino,nlink=s.st_nlink,size=s.st_size,mtime_ns=s.st_mtime_ns)
  if stat.S_ISLNK(s.st_mode): row['target']=os.readlink(path)
  elif stat.S_ISREG(s.st_mode): row['data']=open(path,'rb').read(256).decode('utf-8',errors='backslashreplace')
  rows.append(row)
print(json.dumps(sorted(rows,key=lambda r:r['path'])))
`

func (p *metadataProbe) list(path string, recursive bool) {
	s, err := p.c.Stub.VolumeListFiles2(context.Background(), pb.VolumeListFiles2Request_builder{VolumeId: p.vol, Path: path, Recursive: recursive}.Build())
	var rows []json.RawMessage
	if err == nil {
		for {
			m, e := s.Recv()
			if e == io.EOF {
				break
			}
			if e != nil {
				err = e
				break
			}
			for _, entry := range m.GetEntries() {
				rows = append(rows, json.RawMessage(protojson.Format(entry)))
			}
		}
	}
	p.record(fmt.Sprintf("VolumeListFiles2 path=%q recursive=%v", path, recursive), rows, err)
	if path == "/" || path == "" {
		p.code(err, codes.OK)
		byPath := map[string]map[string]any{}
		for _, raw := range rows {
			var row map[string]any
			if err := json.Unmarshal(raw, &row); err != nil {
				p.t.Fatal(err)
			}
			byPath[row["path"].(string)] = row
		}
		for name, size := range map[string]string{"absolute": "11", "relative": "6", "dangling": "7", "directory": "3", "outside": "13"} {
			row := byPath[name]
			if row["type"] != "SYMLINK" || row["size"] != size || row["mtime"] == nil {
				p.t.Errorf("Modal symlink listing changed: %s: %v; revisit the types and refusals in fs-metadata.md", name, row)
			}
		}
		for name := range byPath {
			if strings.HasPrefix(name, "directory/") {
				p.t.Errorf("Modal recursive listing now follows a directory symlink: %s; revisit fs-metadata.md (recursive listing)", name)
			}
		}
		if byPath["relative"]["mtime"] != "1000000000" {
			p.t.Error("Modal symlink mtime changed; revisit fs-metadata.md")
		}
	} else if path != "dir" && path != "target" {
		p.code(err, codes.FailedPrecondition)
	} else {
		p.code(err, codes.OK)
	}
}

func (p *metadataProbe) put(path, data string, mode *uint32) error {
	// Seed the content through the existing block uploader, then refer to its
	// committed hash in the exact raw request (including optional-mode presence).
	seed := "probe-block"
	if err := put(p.t, p.c, p.vol, seed, []byte(data)); err != nil {
		p.t.Fatal(err)
	}
	f := pb.VolumePutFiles2Request_File_builder{Path: path, Size: uint64(len(data)), Mode: mode}
	for _, h := range HashBlocks([]byte(data)) {
		f.Blocks = append(f.Blocks, pb.VolumePutFiles2Request_Block_builder{ContentsSha256: h}.Build())
	}
	req := pb.VolumePutFiles2Request_builder{VolumeId: p.vol, Files: []*pb.VolumePutFiles2Request_File{f.Build()}}.Build()
	p.record("raw put request", json.RawMessage(protojson.Format(req)), nil)
	r, err := p.c.Stub.VolumePutFiles2(context.Background(), req)
	label := fmt.Sprintf("VolumePutFiles2 path=%q data=%q mode=omitted", path, data)
	if mode != nil {
		label = fmt.Sprintf("VolumePutFiles2 path=%q data=%q mode=%#o", path, data, *mode)
	}
	var result any
	if r != nil {
		result = json.RawMessage(protojson.Format(r))
	}
	p.record(label, result, err)
	if err == nil && len(r.GetMissingBlocks()) != 0 {
		p.t.Fatal("seeded block unexpectedly missing")
	}

	return err
}

// These assertions describe an observed limitation, not desired filesystem
// behavior. A change requires revisiting the design rather than silently
// trusting the fake server or a protobuf field that the service ignores.
func (p *metadataProbe) code(err error, want codes.Code) {
	p.t.Helper()
	if got := status.Code(err); got != want {
		p.t.Errorf("Modal contract changed: got %s (%v), want %s; review fs-metadata.md", got, err, want)
	}
}

type metadataRow struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Ino    uint64 `json:"ino"`
	Nlink  int    `json:"nlink"`
	Size   int64  `json:"size"`
	Mtime  int64  `json:"mtime_ns"`
	Target string `json:"target"`
	Data   string `json:"data"`
}

func (p *metadataProbe) inspect() map[string]metadataRow {
	p.t.Helper()
	var rows []metadataRow
	if err := json.Unmarshal([]byte(p.container(metadataStat)), &rows); err != nil {
		p.t.Fatal(err)
	}
	byPath := make(map[string]metadataRow, len(rows))
	for _, r := range rows {
		byPath[r.Path] = r
	}
	return byPath
}

func (p *metadataProbe) file(rows map[string]metadataRow, path, mode, data string, nlink int) {
	p.t.Helper()
	r, ok := rows[path]
	if !ok || r.Mode != mode || r.Data != data || r.Nlink != nlink {
		p.t.Errorf("Modal contract changed for %s: %+v, want mode=%s data=%q nlink=%d; revisit fs-metadata.md (permissions, hard links)", path, r, mode, data, nlink)
	}
}

func TestContractFilesystemMetadata(t *testing.T) {
	c, vol := scratch(t)
	cfg, err := LoadConfig(os.Getenv("MODALFS_E2E_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := modal.NewClientWithOptions(&modal.ClientParams{TokenID: cfg.TokenID, TokenSecret: cfg.TokenSecret, Environment: cfg.Environment})
	if err != nil {
		t.Fatal(err)
	}
	defer sdk.Close()
	app, err := sdk.Apps.FromName(context.Background(), fmt.Sprintf("modalfs-metadata-%d", time.Now().UnixNano()), &modal.AppFromNameParams{Environment: cfg.Environment, CreateIfMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.StopApp(context.Background(), app.AppID); err != nil {
			t.Error(err)
		}
	})
	p := &metadataProbe{t: t, c: c, sdk: sdk, app: app, vol: vol}
	artifact := os.Getenv("MODALFS_METADATA_ARTIFACT")
	if artifact == "" {
		artifact = filepath.Join(t.TempDir(), "fs-metadata.json")
	}
	defer func() {
		b, _ := json.MarshalIndent(map[string]any{"volume": vol, "profile": cfg.Profile, "observed_at": time.Now().UTC(), "steps": p.steps}, "", "  ")
		if err := os.MkdirAll(filepath.Dir(artifact), 0o700); err != nil {
			t.Error(err)
			return
		}
		if err := os.WriteFile(artifact, b, 0o600); err != nil {
			t.Error(err)
		}
		t.Log("artifact: " + artifact)
	}()
	p.container(`import os
os.mkdir('/vol/dir')
for name in ['target','hard','exec','oldtime']:
 open('/vol/'+name,'w').write('original-'+name)
os.link('/vol/hard','/vol/alias')
os.chmod('/vol/exec',0o751)
os.utime('/vol/oldtime',(946684800,946684800))
for name,target in [('absolute','/vol/target'),('relative','target'),('dangling','missing'),('directory','dir'),('outside','/etc/hostname')]:
 os.symlink(target,'/vol/'+name)
os.utime('/vol/relative',(1000000000,1000000000),follow_symlinks=False)
for op in ['put','copy','remove']:
 os.symlink('target','/vol/link-'+op)
open('/vol/dir/child','w').write('child')
os.mkdir('/vol/tree')
open('/vol/tree/file','w').write('tree-file')
os.symlink('file','/vol/tree/link')
os.mkdir('/vol/delete-tree')
os.symlink('/vol/target','/vol/delete-tree/link')
for name in ['remove-hard','over-hard']:
 open('/vol/'+name,'w').write('original-'+name)
 os.link('/vol/'+name,'/vol/'+name+'-alias')
`)
	initial := p.inspect()
	p.file(initial, "hard", "0o100644", "original-hard", 2)
	if initial["hard"].Ino != initial["alias"].Ino {
		t.Fatal("fixture is not hard linked")
	}
	p.file(initial, "exec", "0o100751", "original-exec", 1)
	data, readErr := c.ReadAll(context.Background(), vol, "target")
	p.record("VolumeGetFile2 target before copy", string(data), readErr)
	p.code(readErr, codes.OK)
	if string(data) != "original-target" {
		t.Fatal("container data not published")
	}
	p.list("/", false)
	p.list("", false)
	p.list("dir", false)
	p.list("target", false)
	p.list("/", true)
	for _, path := range []string{"absolute", "relative", "dangling", "directory", "outside", "directory/child"} {
		p.list(path, false)
		p.list(path, true)
		data, err := c.ReadAll(context.Background(), vol, path)
		p.record("VolumeGetFile2 "+path, string(data), err)
		p.code(err, codes.FailedPrecondition)
	}
	ctx := context.Background()
	for _, path := range []string{"absolute", "relative", "dangling", "directory", "outside"} {
		err := c.Copy(ctx, vol, []string{path}, "copy-"+path, true)
		p.record("VolumeCopyFiles2 "+path+" -> copy-"+path, nil, err)
		p.code(err, codes.FailedPrecondition)
		p.code(p.put(path, "replace-link", nil), codes.FailedPrecondition)
		err = c.Remove(ctx, vol, path, false)
		p.record("VolumeRemoveFile2 "+path, nil, err)
		p.code(err, codes.FailedPrecondition)
	}
	err = c.Copy(ctx, vol, []string{"hard"}, "hard-copy", false)
	p.record("VolumeCopyFiles2 hard -> hard-copy", nil, err)
	p.code(err, codes.OK)
	err = c.Copy(ctx, vol, []string{"exec"}, "exec-copy", false)
	p.record("VolumeCopyFiles2 exec -> exec-copy", nil, err)
	p.code(err, codes.OK)
	err = c.Copy(ctx, vol, []string{"exec"}, "link-copy", false)
	p.record("VolumeCopyFiles2 exec -> link-copy", nil, err)
	p.code(err, codes.FailedPrecondition)
	err = c.Remove(ctx, vol, "link-remove", false)
	p.record("VolumeRemoveFile2 link-remove", nil, err)
	p.code(err, codes.FailedPrecondition)
	p.code(p.put("link-put", "put-to-link", nil), codes.FailedPrecondition)
	p.code(p.put("directory/new", "beneath-link", nil), codes.FailedPrecondition)
	err = c.Remove(ctx, vol, "directory/child", false)
	p.record("VolumeRemoveFile2 directory/child", nil, err)
	p.code(err, codes.FailedPrecondition)
	p.code(p.put("tree-copy/.keep", "", nil), codes.OK)
	_, err = c.Stub.VolumeCopyFiles2(ctx, pb.VolumeCopyFiles2Request_builder{VolumeId: vol, SrcPaths: []string{"tree"}, DstPath: "tree-copy/", Recursive: true}.Build())
	p.record("VolumeCopyFiles2 tree -> tree-copy recursive", nil, err)
	p.code(err, codes.Internal)
	p.inspect()
	err = c.Remove(ctx, vol, "tree-copy", true)
	p.record("VolumeRemoveFile2 tree-copy recursive", nil, err)
	p.code(err, codes.OK)
	err = c.Remove(ctx, vol, "delete-tree", true)
	p.record("VolumeRemoveFile2 delete-tree containing outside symlink recursive", nil, err)
	p.code(err, codes.OK)
	err = c.Remove(ctx, vol, "remove-hard-alias", false)
	p.record("VolumeRemoveFile2 remove-hard-alias (still linked)", nil, err)
	p.code(err, codes.OK)
	err = c.Copy(ctx, vol, []string{"exec"}, "over-hard-alias", false)
	p.record("VolumeCopyFiles2 exec -> over-hard-alias (still linked)", nil, err)
	p.code(err, codes.OK)
	copies := p.inspect()
	p.file(copies, "exec-copy", "0o100751", "original-exec", 1)
	if copies["exec-copy"].Mtime <= initial["exec"].Mtime {
		t.Error("Modal copy mtime behavior changed; revisit modification times in fs-metadata.md")
	}
	p.file(copies, "hard-copy", "0o100644", "original-hard", 1)
	p.file(copies, "hard", "0o100644", "original-hard", 2)
	p.file(copies, "remove-hard", "0o100644", "original-remove-hard", 1)
	p.file(copies, "over-hard", "0o100644", "original-over-hard", 1)
	p.file(copies, "over-hard-alias", "0o100751", "original-exec", 1)
	p.code(p.put("alias", "hard-update", nil), codes.OK)
	p.code(p.put("exec", "exec-update", nil), codes.OK)
	p.code(p.put("oldtime", "time-update", nil), codes.OK)
	for _, mode := range []uint32{0, 0o751, 0o7777, 0o100751, 0o120777, 0o040755, 0o010644} {
		m := mode
		p.code(p.put(fmt.Sprintf("mode-%o", mode), "target", &m), codes.OK)
	}
	updated := p.inspect()
	p.file(updated, "hard", "0o100644", "original-hard", 1)
	p.file(updated, "alias", "0o100666", "hard-update", 1)
	p.file(updated, "exec", "0o100666", "exec-update", 1)
	if updated["oldtime"].Mtime <= 946684800000000000 {
		t.Error("Modal upload mtime behavior changed; revisit modification times in fs-metadata.md")
	}
	for _, mode := range []uint32{0, 0o751, 0o7777, 0o100751, 0o120777, 0o040755, 0o010644} {
		p.file(updated, fmt.Sprintf("mode-%o", mode), "0o100666", "target", 1)
	}
	p.code(p.put("mode-751", "mode-omitted-update", nil), codes.OK)
	m := uint32(0o700)
	p.code(p.put("hard", "hard-with-mode", &m), codes.OK)
	p.file(p.inspect(), "hard", "0o100666", "hard-with-mode", 1)
	err = c.Remove(ctx, vol, "alias", false)
	p.record("VolumeRemoveFile2 alias", nil, err)
	p.code(err, codes.OK)
	err = c.Copy(ctx, vol, []string{"exec-copy"}, "hard", false)
	p.record("VolumeCopyFiles2 exec-copy -> hard (rename-over step)", nil, err)
	p.code(err, codes.OK)
	final := p.inspect()
	p.file(final, "mode-751", "0o100666", "mode-omitted-update", 1)
	p.file(final, "hard", "0o100751", "original-exec", 1)
	for name, target := range map[string]string{"absolute": "/vol/target", "relative": "target", "dangling": "missing", "directory": "dir", "outside": "/etc/hostname", "link-put": "target", "link-copy": "target", "link-remove": "target"} {
		r := final[name]
		if r.Mode != "0o120777" || r.Target != target {
			t.Errorf("Modal symlink mutation behavior changed: %+v; revisit the refusals in fs-metadata.md", r)
		}
	}
	p.file(final, "target", "0o100644", "original-target", 1)
	for _, path := range []string{"alias", "remove-hard-alias", "tree-copy", "delete-tree", "copy-absolute", "copy-relative", "copy-dangling", "copy-directory", "copy-outside"} {
		if _, ok := final[path]; ok {
			t.Errorf("unexpected surviving path: %s", path)
		}
	}
	p.list("/", true)
}
