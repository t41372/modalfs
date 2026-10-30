module github.com/t41372/modalfs

go 1.27.3

require (
	github.com/go-git/go-billy/v5 v5.9.2
	github.com/hanwen/go-fuse/v2 v2.12.1
	github.com/hashicorp/golang-lru/v2 v2.0.7
	github.com/modal-labs/modal-client/go v0.11.1
	github.com/pelletier/go-toml/v2 v2.4.3
	github.com/wailsapp/wails/v3 v3.0.0
	github.com/willscott/go-nfs v0.0.4
	github.com/willscott/go-nfs-client v0.0.0-20251022144359-801f10d98886
	github.com/zalando/go-keyring v0.2.8
	go.etcd.io/bbolt v1.5.0
	golang.org/x/crypto v0.58.0
	golang.org/x/sync v0.24.0
	golang.org/x/sys v0.49.0
	golang.org/x/term v0.47.0
	google.golang.org/grpc v1.85.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/aristanetworks/gomap v0.0.0-20240919214256-2b26376628e1 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/fxamacker/cbor/v2 v2.9.6 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.4 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kisielk/og-rek v1.3.0 // indirect
	github.com/mattn/go-colorable v0.1.16 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/rasky/go-xdr v0.0.0-20170124162913-1a41d1a06c93 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261026165753-2ffab32bce7e // indirect
)

replace github.com/willscott/go-nfs => ./third_party/go-nfs
