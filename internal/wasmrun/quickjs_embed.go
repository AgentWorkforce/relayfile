package wasmrun

import _ "embed"

//go:embed quickjs.wasm
var quickJSWASM []byte

func QuickJSWASM() ([]byte, error) {
	if len(quickJSWASM) == 0 {
		return nil, ErrNoQuickJS
	}
	return append([]byte(nil), quickJSWASM...), nil
}
