.PHONY: build bindings compile clean

build: compile
	wasm-tools component embed ./wit module.wasm --output embed.wasm
	wasm-tools component new embed.wasm -o component.wasm --adapt adapters/tier1/wasi_snapshot_preview1.wasm

bindings:
	wit-bindgen tiny-go --world component-three --out-dir binding ./wit

compile: bindings
	tinygo build -target=wasi -tags=purego -o module.wasm main.go

clean:
	rm -rf status
	rm *.wasm
