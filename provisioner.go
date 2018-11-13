package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/crypto/ssh"

	"github.com/hashicorp/packer/common"
	"github.com/hashicorp/packer/helper/config"
	"github.com/hashicorp/packer/packer"
	"github.com/hashicorp/packer/template/interpolate"
)

type Config struct {
	common.PackerConfig `mapstructure:",squash"`
	ctx                 interpolate.Context

	// The command to run inspec
	Command    string
	SubCommand string

	// Extra options to pass to the inspec command
	ExtraArguments []string `mapstructure:"extra_arguments"`

	// The main test path to execute.
	TestPath             string   `mapstructure:"test_path"`
	Attrs                []string `mapstructure:"attrs"`
	Controls             []string `mapstructure:"controls"`
	Reporter             []string `mapstructure:"reporter"`
	User                 string   `mapstructure:"user"`
	LocalPort            string   `mapstructure:"local_port"`
	SSHHostKeyFile       string   `mapstructure:"ssh_host_key_file"`
	SSHAuthorizedKeyFile string   `mapstructure:"ssh_authorized_key_file"`
	ProfilesPath         string   `mapstructure:"profiles_path"`
	JsonConfig           string   `mapstructure:"json_config"`
}

type Provisioner struct {
	config           Config
	adapter          *adapter
	done             chan struct{}
	inspecVersion    string
	inspecMajVersion uint
}

func (p *Provisioner) Prepare(raws ...interface{}) error {
	p.done = make(chan struct{})

	err := config.Decode(&p.config, &config.DecodeOpts{
		Interpolate:        true,
		InterpolateContext: &p.config.ctx,
		InterpolateFilter: &interpolate.RenderFilter{
			Exclude: []string{},
		},
	}, raws...)
	if err != nil {
		return err
	}

	// Defaults
	if p.config.Command == "" {
		p.config.Command = "inspec"
	}

	if p.config.SubCommand == "" {
		p.config.SubCommand = "exec"
	}

	var errs *packer.MultiError
	err = validateTestPathConfig(p.config.TestPath)
	if err != nil {
		errs = packer.MultiErrorAppend(errs, err)
	}

	// Check that the authorized key file exists
	if len(p.config.SSHAuthorizedKeyFile) > 0 {
		err = validateFileConfig(p.config.SSHAuthorizedKeyFile, "ssh_authorized_key_file", true)
		if err != nil {
			log.Println(p.config.SSHAuthorizedKeyFile, "does not exist")
			errs = packer.MultiErrorAppend(errs, err)
		}
	}
	if len(p.config.SSHHostKeyFile) > 0 {
		err = validateFileConfig(p.config.SSHHostKeyFile, "ssh_host_key_file", true)
		if err != nil {
			log.Println(p.config.SSHHostKeyFile, "does not exist")
			errs = packer.MultiErrorAppend(errs, err)
		}
	}

	if len(p.config.LocalPort) > 0 {
		if _, err := strconv.ParseUint(p.config.LocalPort, 10, 16); err != nil {
			errs = packer.MultiErrorAppend(errs, fmt.Errorf("local_port: %s must be a valid port", p.config.LocalPort))
		}
	} else {
		p.config.LocalPort = "0"
	}

	if len(p.config.Attrs) > 0 {
		for _, attr := range p.config.Attrs {
			err = validateAttrsConfig(attr)
			if err != nil {
				log.Println(attr, "does not exist")
				errs = packer.MultiErrorAppend(errs, err)
			}
		}
	}

	if len(p.config.ProfilesPath) > 0 {
		err = validateProfilesPathConfig(p.config.ProfilesPath)
		if err != nil {
			log.Println(p.config.ProfilesPath, "does not exist")
			errs = packer.MultiErrorAppend(errs, err)
		}
	}

	if p.config.User == "" {
		usr, err := user.Current()
		if err != nil {
			errs = packer.MultiErrorAppend(errs, err)
		} else {
			p.config.User = usr.Username
		}
	}
	if p.config.User == "" {
		errs = packer.MultiErrorAppend(errs, fmt.Errorf("user: could not determine current user from environment."))
	}

	if errs != nil && len(errs.Errors) > 0 {
		return errs
	}
	return nil
}

func (p *Provisioner) getVersion() error {
	out, err := exec.Command(p.config.Command, "version").Output()
	if err != nil {
		return fmt.Errorf(
			"Error running \"%s version\": %s", p.config.Command, err.Error())
	}

	versionRe := regexp.MustCompile(`\w (\d+\.\d+[.\d+]*)`)
	matches := versionRe.FindStringSubmatch(string(out))
	if matches == nil {
		return fmt.Errorf(
			"Could not find %s version in output:\n%s", p.config.Command, string(out))
	}

	version := matches[1]
	log.Printf("%s version: %s", p.config.Command, version)
	p.inspecVersion = version

	majVer, err := strconv.ParseUint(strings.Split(version, ".")[0], 10, 0)
	if err != nil {
		return fmt.Errorf("Could not parse major version from \"%s\".", version)
	}
	p.inspecMajVersion = uint(majVer)

	return nil
}

func (p *Provisioner) Provision(ui packer.Ui, comm packer.Communicator) error {
	ui.Say("Provisioning with Inspec...")

	k, err := newUserKey(p.config.SSHAuthorizedKeyFile)
	if err != nil {
		return err
	}

	hostSigner, err := newSigner(p.config.SSHHostKeyFile)
	// Remove the private key file
	if len(k.privKeyFile) > 0 {
		defer os.Remove(k.privKeyFile)
	}

	keyChecker := ssh.CertChecker{
		UserKeyFallback: func(conn ssh.ConnMetadata, pubKey ssh.PublicKey) (*ssh.Permissions, error) {
			if user := conn.User(); user != p.config.User {
				return nil, errors.New(fmt.Sprintf("authentication failed: %s is not a valid user", user))
			}

			if !bytes.Equal(k.Marshal(), pubKey.Marshal()) {
				return nil, errors.New("authentication failed: unauthorized key")
			}

			return nil, nil
		},
	}

	config := &ssh.ServerConfig{
		AuthLogCallback: func(conn ssh.ConnMetadata, method string, err error) {
			log.Printf("authentication attempt from %s to %s as %s using %s", conn.RemoteAddr(), conn.LocalAddr(), conn.User(), method)
		},
		PublicKeyCallback: keyChecker.Authenticate,
		//NoClientAuth:      true,
	}

	config.AddHostKey(hostSigner)

	localListener, err := func() (net.Listener, error) {
		port, err := strconv.ParseUint(p.config.LocalPort, 10, 16)
		if err != nil {
			return nil, err
		}

		tries := 1
		if port != 0 {
			tries = 10
		}
		for i := 0; i < tries; i++ {
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			port++
			if err != nil {
				ui.Say(err.Error())
				continue
			}
			_, p.config.LocalPort, err = net.SplitHostPort(l.Addr().String())
			if err != nil {
				ui.Say(err.Error())
				continue
			}
			return l, nil
		}
		return nil, errors.New("Error setting up SSH proxy connection")
	}()

	if err != nil {
		return err
	}

	ui = newUi(ui)
	p.adapter = newAdapter(p.done, localListener, config, ui, comm)

	defer func() {
		log.Print("shutting down the SSH proxy")
		close(p.done)
		p.adapter.Shutdown()
	}()

	go p.adapter.Serve()

	if err := p.executeInspec(ui, comm, k.privKeyFile); err != nil {
		return fmt.Errorf("Error executing Inspec: %s", err)
	}

	return nil
}

func (p *Provisioner) Cancel() {
	if p.done != nil {
		close(p.done)
	}
	if p.adapter != nil {
		p.adapter.Shutdown()
	}
	os.Exit(0)
}

func (p *Provisioner) executeInspec(ui packer.Ui, comm packer.Communicator, privKeyFile string) error {
	tests, _ := filepath.Abs(p.config.TestPath)

	args := []string{p.config.SubCommand, tests}
	// TODO: figure out how to pass additional packer vars through inspec
	//args := []string{"--extra-vars", fmt.Sprintf("packer_build_name=%s packer_builder_type=%s",
	//	p.config.PackerBuildName, p.config.PackerBuilderType), tests}
	if len(privKeyFile) > 0 {
		args = append(args, "-i", privKeyFile)
	}

	if len(p.config.ProfilesPath) > 0 {
		args = append(args, "--profiles-path", p.config.ProfilesPath)
	}

	if len(p.config.Attrs) > 0 {
		var attr_args []string
		attr_args = append(attr_args, "--attrs")

		for _, attr := range p.config.Attrs {
			attr_path, _ := filepath.Abs(attr)
			attr_args = append(attr_args, attr_path)
		}

		args = append(args, attr_args...)
	}

	args = append(args, p.config.ExtraArguments...)

	// -t ssh://user@host:port
	args = append(args, "-t", fmt.Sprintf("ssh://%s@127.0.0.1:%s", p.config.User, p.config.LocalPort))

	cmd := exec.Command(p.config.Command, args...)

	cmd.Env = os.Environ()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	wg := sync.WaitGroup{}
	repeat := func(r io.ReadCloser) {
		reader := bufio.NewReader(r)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				line = strings.TrimRightFunc(line, unicode.IsSpace)
				ui.Message(line)
			}
			if err != nil {
				if err == io.EOF {
					break
				} else {
					ui.Error(err.Error())
					break
				}
			}
		}
		wg.Done()
	}
	wg.Add(2)
	go repeat(stdout)
	go repeat(stderr)

	ui.Say(fmt.Sprintf("Executing Inspec: %s", strings.Join(cmd.Args, " ")))
	if err := cmd.Start(); err != nil {
		return err
	}
	wg.Wait()
	err = cmd.Wait()
	if err != nil {
		return fmt.Errorf("Non-zero exit status: %s", err)
	}

	return nil
}

func validateFileConfig(name string, config string, req bool) error {
	if req {
		if name == "" {
			return fmt.Errorf("%s must be specified.", config)
		}
	}
	info, err := os.Stat(name)
	if err != nil {
		return fmt.Errorf("%s: %s is invalid: %s", config, name, err)
	} else if info.IsDir() {
		return fmt.Errorf("%s: %s must point to a file", config, name)
	}
	return nil
}

func validateAttrsConfig(name string) error {
	_, err := os.Stat(name)
	if os.IsNotExist(err) {
		return fmt.Errorf("attrs: %s does not exist: %s", name, err)
	}
	return nil
}

func validateTestPathConfig(name string) error {
	_, err := os.Stat(name)
	if os.IsNotExist(err) {
		return fmt.Errorf("test_path: %s does not exist: %s", name, err)
	}
	return nil
}

func validateProfilesPathConfig(name string) error {
	info, err := os.Stat(name)
	if err != nil {
		return fmt.Errorf("profiles_path: %s is invalid: %s", name, err)
	} else if !info.IsDir() {
		return fmt.Errorf("profiles_path: %s must point to a directory", name)
	}
	return nil
}

type userKey struct {
	ssh.PublicKey
	privKeyFile string
}

func newUserKey(pubKeyFile string) (*userKey, error) {
	userKey := new(userKey)
	if len(pubKeyFile) > 0 {
		pubKeyBytes, err := ioutil.ReadFile(pubKeyFile)
		if err != nil {
			return nil, errors.New("Failed to read public key")
		}
		userKey.PublicKey, _, _, _, err = ssh.ParseAuthorizedKey(pubKeyBytes)
		if err != nil {
			return nil, errors.New("Failed to parse authorized key")
		}

		return userKey, nil
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, errors.New("Failed to generate key pair")
	}
	userKey.PublicKey, err = ssh.NewPublicKey(key.Public())
	if err != nil {
		return nil, errors.New("Failed to extract public key from generated key pair")
	}

	// To support Inspec calling back to us we need to write
	// this file down
	privateKeyDer := x509.MarshalPKCS1PrivateKey(key)
	privateKeyBlock := pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   privateKeyDer,
	}
	tf, err := ioutil.TempFile("", "inspec-key")
	if err != nil {
		return nil, errors.New("failed to create temp file for generated key")
	}
	_, err = tf.Write(pem.EncodeToMemory(&privateKeyBlock))
	if err != nil {
		return nil, errors.New("failed to write private key to temp file")
	}

	err = tf.Close()
	if err != nil {
		return nil, errors.New("failed to close private key temp file")
	}
	userKey.privKeyFile = tf.Name()

	return userKey, nil
}

type signer struct {
	ssh.Signer
}

func newSigner(privKeyFile string) (*signer, error) {
	signer := new(signer)

	if len(privKeyFile) > 0 {
		privateBytes, err := ioutil.ReadFile(privKeyFile)
		if err != nil {
			return nil, errors.New("Failed to load private host key")
		}

		signer.Signer, err = ssh.ParsePrivateKey(privateBytes)
		if err != nil {
			return nil, errors.New("Failed to parse private host key")
		}

		return signer, nil
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, errors.New("Failed to generate server key pair")
	}

	signer.Signer, err = ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, errors.New("Failed to extract private key from generated key pair")
	}

	return signer, nil
}

// Ui provides concurrency-safe access to packer.Ui.
type Ui struct {
	sem chan int
	ui  packer.Ui
}

func newUi(ui packer.Ui) packer.Ui {
	return &Ui{sem: make(chan int, 1), ui: ui}
}

func (ui *Ui) Ask(s string) (string, error) {
	ui.sem <- 1
	ret, err := ui.ui.Ask(s)
	<-ui.sem

	return ret, err
}

func (ui *Ui) Say(s string) {
	ui.sem <- 1
	ui.ui.Say(s)
	<-ui.sem
}

func (ui *Ui) Message(s string) {
	ui.sem <- 1
	ui.ui.Message(s)
	<-ui.sem
}

func (ui *Ui) Error(s string) {
	ui.sem <- 1
	ui.ui.Error(s)
	<-ui.sem
}

func (ui *Ui) Machine(t string, args ...string) {
	ui.sem <- 1
	ui.ui.Machine(t, args...)
	<-ui.sem
}

func (ui *Ui) ProgressBar() packer.ProgressBar {
	return new(packer.NoopProgressBar)
}
