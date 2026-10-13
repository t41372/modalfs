module github.com/t41372/modalfs

go 1.26.0

require (
	github.com/go-git/go-billy/v5 v5.9.2
	github.com/hanwen/go-fuse/v2 v2.12.0
	github.com/hashicorp/golang-lru/v2 v2.0.7
	github.com/modal-labs/modal-client/go v0.11.0
	github.com/pelletier/go-toml/v2 v2.4.3
	github.com/wailsapp/wails/v2 v2.16.0
	github.com/willscott/go-nfs v0.0.4
	github.com/willscott/go-nfs-client v0.0.0-20240104095149-b44639837b00
	github.com/zalando/go-keyring v0.2.8
	go.etcd.io/bbolt v1.5.0
	golang.org/x/crypto v0.55.0
	golang.org/x/sync v0.24.0
	golang.org/x/sys v0.49.0
	golang.org/x/term v0.47.0
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.11
)

require (
	git.sr.ht/~jackmordaunt/go-toast/v2 v2.0.3 // indirect
	github.com/aristanetworks/gomap v0.0.0-20230726210543-f4e41046dced // indirect
	github.com/bep/debounce v1.2.1 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/fxamacker/cbor/v2 v2.9.0 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/jchv/go-winloader v0.0.0-20210711035445-715c2860da7e // indirect
	github.com/kisielk/og-rek v1.3.0 // indirect
	github.com/labstack/echo/v4 v4.13.3 // indirect
	github.com/labstack/gommon v0.4.2 // indirect
	github.com/leaanthony/go-ansi-parser v1.6.1 // indirect
	github.com/leaanthony/gosod v1.0.4 // indirect
	github.com/leaanthony/slicer v1.6.0 // indirect
	github.com/leaanthony/u v1.1.1 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/rasky/go-xdr v0.0.0-20170124162913-1a41d1a06c93 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/samber/lo v1.49.1 // indirect
	github.com/tkrajina/go-reflector v0.5.8 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasttemplate v1.2.2 // indirect
	github.com/wailsapp/go-webview2 v1.0.22 // indirect
	github.com/wailsapp/mimetype v1.4.1 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/exp v0.0.0-20260410095643-746e56fc9e2f // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260706201446-f0a921348800 // indirect
)

replace github.com/willscott/go-nfs => ./third_party/go-nfs
