package backend

type BACKEND_TYPE string

const (
	LLVM  BACKEND_TYPE = "llvm"
	WASM  BACKEND_TYPE = "wasm"
)