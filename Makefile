.PHONY: buf-gen
proto-gen:
	@buf generate "https://github.com/hashicorp/consul.git#branch=main,subdir=proto-public" \
		--template buf.gen.yaml \
		--path pbacl --path pbserverdiscovery
