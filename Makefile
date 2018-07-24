default: build

prepare:
	go get ./...

test: prepare
	go test ./...

fmt:
	go fmt .

build: fmt test
	go build -v

install: build
	mkdir -p ~/.packer.d/plugins
	install ./packer-provisioner-inspec ~/.packer.d/plugins/

release: test
	go get github.com/mitchellh/gox
	gox --output 'dist/{{.OS}}_{{.Arch}}/{{.Dir}}'
	zip -j releases/packer-provisioner-inspec_darwin_386.zip    dist/darwin_386/packer-provisioner-inspec
	zip -j releases/packer-provisioner-inspec_darwin_amd64.zip  dist/darwin_amd64/packer-provisioner-inspec
	zip -j releases/packer-provisioner-inspec_freebsd_386.zip   dist/freebsd_386/packer-provisioner-inspec
	zip -j releases/packer-provisioner-inspec_freebsd_amd64.zip dist/freebsd_amd64/packer-provisioner-inspec
	zip -j releases/packer-provisioner-inspec_freebsd_arm.zip   dist/freebsd_arm/packer-provisioner-inspec
	zip -j releases/packer-provisioner-inspec_linux_386.zip     dist/linux_386/packer-provisioner-inspec
	zip -j releases/packer-provisioner-inspec_linux_amd64.zip   dist/linux_amd64/packer-provisioner-inspec
	zip -j releases/packer-provisioner-inspec_linux_arm.zip     dist/linux_arm/packer-provisioner-inspec
	zip -j releases/packer-provisioner-inspec_netbsd_386.zip    dist/netbsd_386/packer-provisioner-inspec
	zip -j releases/packer-provisioner-inspec_netbsd_amd64.zip  dist/netbsd_amd64/packer-provisioner-inspec
	zip -j releases/packer-provisioner-inspec_netbsd_arm.zip    dist/netbsd_arm/packer-provisioner-inspec
	zip -j releases/packer-provisioner-inspec_openbsd_386.zip   dist/openbsd_386/packer-provisioner-inspec
	zip -j releases/packer-provisioner-inspec_openbsd_amd64.zip dist/openbsd_amd64/packer-provisioner-inspec
	zip -j releases/packer-provisioner-inspec_windows_386.zip   dist/windows_386/packer-provisioner-inspec.exe
	zip -j releases/packer-provisioner-inspec_windows_amd64.zip dist/windows_amd64/packer-provisioner-inspec.exe

clean:
	rm -rf dist/
	rm -f releases/*.zip

.PHONY: default prepare test build install release clean