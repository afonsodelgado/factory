package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/state"
)

const pushKeyUsage = "usage: factory node push-key export --file <path> | import [--file <path>] [--replace]"

func runPushKey(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, pushKeyUsage)
		return 2
	}
	flags := flag.NewFlagSet("node push-key", flag.ContinueOnError)
	file := flags.String("file", "", "the key file: a new file to export to, or one to import from (standard input without it)")
	replace := flags.Bool("replace", false, "switch to the imported key even though phones are subscribed to this node's own key")
	exporting := args[0] == "export"
	usable := flags.Parse(args[1:]) == nil && flags.NArg() == 0 && (exporting && *file != "" && !*replace || args[0] == "import")
	if !usable {
		fmt.Fprintln(os.Stderr, pushKeyUsage)
		return 2
	}
	cfg, err := state.LoadConfig()
	if err == nil {
		store := phone.Open(cfg.StateDir)
		if exporting {
			err = exportPushKey(store, *file, os.Stdout)
		} else {
			err = importPushKey(store, *file, *replace, os.Stdin, os.Stdout)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory node push-key:", err)
		return 1
	}
	return 0
}

func exportPushKey(store phone.Store, path string, out io.Writer) error {
	shared, err := store.Export()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(shared, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already exists; export writes only a new file, so pick another path", path)
	}
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	if err := errors.Join(err, f.Close()); err != nil {
		os.Remove(path)
		return err
	}
	fmt.Fprintf(out, "exported  push key %s to %s, readable only by you. It holds the private key: copy it to the other node, run factory node push-key import --file there, then delete it.\n", shared.Fingerprint(), path)
	return nil
}

func importPushKey(store phone.Store, path string, replace bool, stdin io.Reader, out io.Writer) error {
	data, err := readKeyFile(path, stdin)
	if err != nil {
		return err
	}
	var shared phone.Shared
	if err := json.Unmarshal(data, &shared); err != nil || shared.Private == "" {
		return errors.New("this is not a file from factory node push-key export")
	}
	current, err := store.KeyState()
	if err != nil {
		return err
	}
	incoming := shared.Fingerprint()
	silencesPhones := current.Public != "" && current.Fingerprint != incoming && current.Phones > 0
	if silencesPhones && !replace {
		return fmt.Errorf("this node sends with push key %s, and %s subscribed to it; importing %s would stop notifying them. Run it again with --replace to switch; those phones then subscribe again", current.Fingerprint, phoneCount(current.Phones), incoming)
	}
	if err := store.Stage(shared, replace, time.Now()); err != nil {
		return err
	}
	now, err := store.Promote()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "imported  push key %s with %s. The node sends with it from now on.\n", now.Fingerprint, phoneCount(now.Phones))
	return nil
}

func readKeyFile(path string, stdin io.Reader) ([]byte, error) {
	if path == "" || path == "-" {
		return io.ReadAll(io.LimitReader(stdin, 1<<20))
	}
	return os.ReadFile(path)
}

func phoneCount(n int) string {
	if n == 1 {
		return "1 phone subscribed"
	}
	return fmt.Sprintf("%d phones subscribed", n)
}
