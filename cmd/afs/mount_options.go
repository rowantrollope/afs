package main

import (
	"flag"
	"fmt"
	"strconv"
)

// These are per-mount choices. Pointers distinguish an explicit root uid/gid
// from an omitted override, including after registry/bootstrap serialization.
type mountOptions struct {
	ReadOnly   bool
	UID        *uint32
	GID        *uint32
	AllowOther bool
}

func addMountOptions(flags *flag.FlagSet) *mountOptions {
	opts := &mountOptions{}
	flags.BoolVar(&opts.ReadOnly, "readonly", false, "receive remote changes without publishing local changes")
	flags.BoolVar(&opts.AllowOther, "allow-other", false, "allow other local users to access a FUSE mount")
	for name, target := range map[string]**uint32{"uid": &opts.UID, "gid": &opts.GID} {
		name, target := name, target
		flags.Func(name, "FUSE ownership override (unsigned 32-bit integer)", func(value string) error {
			parsed, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return fmt.Errorf("--%s requires an integer from 0 to 4294967295", name)
			}
			id := uint32(parsed)
			*target = &id
			return nil
		})
	}
	return opts
}

func validateMountOptions(flags *flag.FlagSet, backend string) error {
	if backend != "sync" && backend != "fuse" && backend != "nfs" {
		return fmt.Errorf("unknown mount backend %q; choose sync, fuse, or nfs", backend)
	}
	var err error
	flags.Visit(func(f *flag.Flag) {
		if backend != "fuse" && (f.Name == "uid" || f.Name == "gid" || f.Name == "allow-other") {
			err = fmt.Errorf("--%s requires --backend fuse", f.Name)
		}
	})
	return err
}
