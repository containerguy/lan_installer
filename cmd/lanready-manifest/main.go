package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/containerguy/lan_installer/internal/model"
	"github.com/containerguy/lan_installer/internal/signing"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "sign":
		err = sign(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func keygen(args []string) error {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	privatePath := flags.String("private-key", "lanready-private.key", "Ausgabedatei für Private Key")
	publicPath := flags.String("public-key", "lanready-public.key", "Ausgabedatei für Public Key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if _, err := os.Stat(*privatePath); err == nil {
		return fmt.Errorf("%s already exists", *privatePath)
	}
	publicKey, privateKey, err := signing.Generate()
	if err != nil {
		return err
	}
	if err := signing.WriteKey(*privatePath, privateKey, 0o600); err != nil {
		return err
	}
	if err := signing.WriteKey(*publicPath, publicKey, 0o644); err != nil {
		_ = os.Remove(*privatePath)
		return err
	}
	fmt.Printf("Keys created: %s, %s\n", *privatePath, *publicPath)
	return nil
}

func sign(args []string) error {
	flags := flag.NewFlagSet("sign", flag.ContinueOnError)
	input := flags.String("in", "manifest.json", "Manifest")
	output := flags.String("out", "envelope.json", "Signiertes Envelope")
	privatePath := flags.String("private-key", "lanready-private.key", "Private Key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	raw, err := os.ReadFile(*input)
	if err != nil {
		return err
	}
	var manifest model.EventManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	if err := signing.Validate(manifest); err != nil {
		return err
	}
	privateKey, err := signing.ReadPrivateKey(*privatePath)
	if err != nil {
		return err
	}
	envelope, err := signing.Sign(raw, privateKey)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*output, append(envelope, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("Signed manifest written to %s\n", *output)
	return nil
}

func verify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	input := flags.String("in", "envelope.json", "Signiertes Envelope")
	publicPath := flags.String("public-key", "lanready-public.key", "Public Key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	envelope, err := os.ReadFile(*input)
	if err != nil {
		return err
	}
	publicKey, err := signing.ReadPublicKey(*publicPath)
	if err != nil {
		return err
	}
	manifest, err := signing.Verify(envelope, ed25519.PublicKey(publicKey))
	if err != nil {
		return err
	}
	if manifest.ID == "" {
		return errors.New("verified manifest is empty")
	}
	fmt.Printf("Valid manifest: %s (%s)\n", manifest.Name, manifest.ID)
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: lanready-manifest <keygen|sign|verify> [options]")
	os.Exit(2)
}
