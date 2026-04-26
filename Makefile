.PHONY: build install dev

# Build the binary
build:
	go build -o cpt .

# Build and install to ~/bin + register shell widget
install: build
	mkdir -p ~/bin
	cp cpt ~/bin/cpt
	./cpt --install
	@echo ""
	@echo "Run: source ~/.zshrc (or restart your terminal)"

# Dev: build and test inline
dev: build
	./cpt
