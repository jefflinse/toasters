package defaults

import "embed"

//go:embed system
var SystemFiles embed.FS

//go:embed user
var UserFiles embed.FS

//go:embed config.yaml
var DefaultConfig []byte
