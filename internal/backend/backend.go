package backend

type BackendType string

const (
	BackendLLVM BackendType = "llvm"
	BackendWASM BackendType = "wasm"
)
