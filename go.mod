module git.thebytes.net/roberts/broadside

// Go 1.25 is the floor. os.Root arrived in 1.24 and is what makes path
// traversal structurally impossible in the storage layer, but Root.Rename only
// landed in 1.25. Without it, the atomic write-then-rename that every post save
// depends on would have to fall back to os.Rename on a resolved path, which
// gives up the confinement guarantee at the exact moment it matters most.
go 1.25.0

require (
	github.com/fsnotify/fsnotify v1.10.1
	github.com/microcosm-cc/bluemonday v1.0.27
	github.com/yuin/goldmark v1.8.5
	golang.org/x/crypto v0.54.0
)

require (
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)
