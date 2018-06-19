Packer InSpec Provisioner
=======

packer-provisioner-inspec is a [Packer](https://packer.io/) plugin that
tests machines during provisioning using [InSpec](https://www.inspec.io/docs/).

Install
======

Build and install the plugin (you'll need ```git``` and ```go```):
````Shell
go get -d github.com/jrbeilke/packer-provisioner-inspec
cd $GOPATH/src/github.com/jrbeilke/packer-provisioner-inspec
mkdir $HOME/.packer.d/plugins
go build -o $HOME/.packer.d/plugins/packer-provisioner-inspec
````

Usage
======

Add the provisioner to your packer template:

```json
{
  "variables": {
    "version":  "0.1",
    "box_name": "test-project"
  },
  "builders": [ ... ],
  "provisioners": [{
    "type": "inspec",
    "test_path": "inspec/example.rb"
  }]
}
```

The connection to the images being built is facilitated via a local SSH proxy that InSpec connects to.

Configuration Reference
======

required parameters
------

- `test_path` - The relative path to the InSpec test file or profile to be utilized.

optional parameters
------

- `local_port` (string) - The port on which inspec-provisioner should first
  attempt to listen for SSH connections. This value is a starting point.
	inspec-provisioner will attempt listen for SSH connections on the first
	available of ten ports, starting at `local_port`. When `local_port` is missing
	or empty, inspec-provisioner will listen on a system-chosen port.
- `sftp_command` (string) - The command to run on the machine to handle the
	SFTP protocol that InSpec will use to transfer files. The command should
	read and write on stdin and stdout, respectively. Defaults to
  `/usr/lib/sftp-server -e`.
- `ssh_host_key_file` - The SSH key that will be used to run the SSH server to which InSpec connects.
- `ssh_authorized_key_file` - The SSH public key of the InSpec `ssh_user`.
