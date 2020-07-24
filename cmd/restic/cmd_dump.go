package main

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/restic/restic/internal/debug"
	"github.com/restic/restic/internal/errors"
	"github.com/restic/restic/internal/restic"
	"github.com/restic/restic/internal/walker"

	"github.com/spf13/cobra"
)

var cmdDump = &cobra.Command{
	Use:   "dump [flags] snapshotID file",
	Short: "Print a backed-up file to stdout",
	Long: `
The "dump" command extracts a single file from a snapshot from the repository and
prints its contents to stdout.

The special snapshot "latest" can be used to use the latest snapshot in the
repository.

EXIT STATUS
===========

Exit status is 0 if the command was successful, and non-zero if there was any error.
`,
	DisableAutoGenTag: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDump(dumpOptions, globalOptions, args)
	},
}

// DumpOptions collects all options for the dump command.
type DumpOptions struct {
	Hosts []string
	Paths []string
	Tags  restic.TagLists
}

var dumpOptions DumpOptions

var dumpWriter io.Writer = os.Stdout

func init() {
	cmdRoot.AddCommand(cmdDump)

	flags := cmdDump.Flags()
	flags.StringArrayVarP(&dumpOptions.Hosts, "host", "H", nil, `only consider snapshots for this host when the snapshot ID is "latest" (can be specified multiple times)`)
	flags.Var(&dumpOptions.Tags, "tag", "only consider snapshots which include this `taglist` for snapshot ID \"latest\"")
	flags.StringArrayVar(&dumpOptions.Paths, "path", nil, "only consider snapshots which include this (absolute) `path` for snapshot ID \"latest\"")
}

func splitPath(p string) []string {
	if p == "/" {
		return []string{"/"}
	}

	segments := strings.Split(p, "/")
	if strings.HasPrefix(p, "/") {
		segments[0] = "/"
	}

	return segments
}

func printFromTree(ctx context.Context, tree *restic.Tree, repo restic.Repository, prefix string, pathComponents []string, leadingNodes *[]*restic.Node) error {

	if tree == nil {
		return fmt.Errorf("called with a nil tree")
	}
	if repo == nil {
		return fmt.Errorf("called with a nil repository")
	}
	l := len(pathComponents)
	if l == 0 {
		return fmt.Errorf("empty path components")
	}
	item := filepath.Join(prefix, pathComponents[0])
	for _, node := range tree.Nodes {
		if node.Name == pathComponents[0] || pathComponents[0] == "/" {
			*leadingNodes = append(*leadingNodes, node)
			switch {
			case l == 1 && node.Type == "file":
				return getNodeData(ctx, dumpWriter, repo, node)
			case l > 1 && node.Type == "dir":
				subtree, err := repo.LoadTree(ctx, *node.Subtree)
				if err != nil {
					return errors.Wrapf(err, "cannot load subtree for %q", item)
				}
				return printFromTree(ctx, subtree, repo, item, pathComponents[1:], leadingNodes)
			case node.Type == "dir":
				return tarTree(ctx, repo, *node.Subtree, *leadingNodes)
			case l > 1:
				return fmt.Errorf("%q should be a dir, but is a %q", item, node.Type)
			case node.Type != "file":
				return fmt.Errorf("%q should be a file, but is a %q", item, node.Type)
			}
		}
	}
	return fmt.Errorf("path %q not found in snapshot", item)
}

func runDump(opts DumpOptions, gopts GlobalOptions, args []string) error {
	ctx := gopts.ctx

	if len(args) != 2 {
		return errors.Fatal("no file and no snapshot ID specified")
	}

	snapshotIDString := args[0]
	pathToPrint := args[1]

	debug.Log("dump file %q from %q", pathToPrint, snapshotIDString)

	repo, err := OpenRepository(gopts)
	if err != nil {
		return err
	}

	if !gopts.NoLock {
		lock, err := lockRepo(repo)
		defer unlockRepo(lock)
		if err != nil {
			return err
		}
	}

	err = repo.LoadIndex(ctx)
	if err != nil {
		return err
	}

	var id restic.ID

	if snapshotIDString == "latest" {
		id, err = restic.FindLatestSnapshot(ctx, repo, opts.Paths, opts.Tags, opts.Hosts)
		if err != nil {
			Exitf(1, "latest snapshot for criteria not found: %v Paths:%v Hosts:%v", err, opts.Paths, opts.Hosts)
		}
	} else {
		id, err = restic.FindSnapshot(repo, snapshotIDString)
		if err != nil {
			Exitf(1, "invalid id %q: %v", snapshotIDString, err)
		}
	}

	sn, err := restic.LoadSnapshot(gopts.ctx, repo, id)
	if err != nil {
		Exitf(2, "loading snapshot %q failed: %v", snapshotIDString, err)
	}

	if pathToPrint == "/" {
		return tarTree(ctx, repo, *sn.Tree, nil)
	}

	tree, err := repo.LoadTree(ctx, *sn.Tree)
	if err != nil {
		Exitf(2, "loading tree for snapshot %q failed: %v", snapshotIDString, err)
	}

	err = printFromTree(ctx, tree, repo, "", splitPath(path.Clean(pathToPrint)), new([]*restic.Node))
	if err != nil {
		Exitf(2, "cannot dump file: %v", err)
	}

	return nil
}

func getNodeData(ctx context.Context, output io.Writer, repo restic.Repository, node *restic.Node) error {
	var (
		buf []byte
		err error
	)
	for _, id := range node.Content {
		buf, err = repo.LoadBlob(ctx, restic.DataBlob, id, buf)
		if err != nil {
			return err
		}

		_, err = output.Write(buf)
		if err != nil {
			return errors.Wrap(err, "Write")
		}

	}
	return nil
}

func tarTree(ctx context.Context, repo restic.Repository, id restic.ID, leadingNodes []*restic.Node) error {

	if dumpWriter == os.Stdout && stdoutIsTerminal() {
		return fmt.Errorf("stdout is the terminal, please redirect output")
	}

	tw := tar.NewWriter(dumpWriter)
	defer tw.Close()

	rootPath := ""

	for _, node := range leadingNodes {
		rootPath = filepath.Join(rootPath, node.Name)
		node.Path = rootPath
		if err := tarNode(ctx, tw, node, repo); err != nil {
			return err
		}
	}

	err := walker.Walk(ctx, repo, id, nil, func(_ restic.ID, nodepath string, node *restic.Node, err error) (bool, error) {
		if err != nil {
			return false, err
		}
		if node == nil {
			return false, nil
		}

		// Leading slashes are dangerous in tar archive. The GNU tool will
		// strip them, but we better not rely on that.
		node.Path = strings.TrimLeft(filepath.Join(rootPath, nodepath), "/")

		if node.Type == "file" || node.Type == "symlink" || node.Type == "dir" {
			err := tarNode(ctx, tw, node, repo)
			if err != err {
				return false, err
			}
		}

		return false, nil
	})

	return err
}

func tarNode(ctx context.Context, tw *tar.Writer, node *restic.Node, repo restic.Repository) error {
	header := &tar.Header{
		Name: node.Path,
		Size: int64(node.Size),
		// Mode fits 21 bits in PAX format (7 octal digits). All higher bits in
		// node.Mode are related to the node type and dealt with below.
		//
		// https://golang.org/pkg/archive/tar/#Format
		Mode:       int64(node.Mode & 07777777),
		Uid:        int(node.UID),
		Gid:        int(node.GID),
		ModTime:    node.ModTime,
		AccessTime: node.AccessTime,
		ChangeTime: node.ChangeTime,

		Format:     tar.FormatPAX,
		PAXRecords: parseXattrs(node.ExtendedAttributes),
	}

	switch node.Type {
	case "dir":
		header.Typeflag = tar.TypeDir
	case "file":
		header.Typeflag = tar.TypeReg
	case "symlink":
		header.Typeflag = tar.TypeSymlink
		header.Linkname = node.LinkTarget
	case "dev":
		header.Typeflag = tar.TypeBlock
		header.Devmajor = int64((node.Device >> 8) & 0xff)
		header.Devminor = int64(node.Device & 0xff)
	case "chardev":
		header.Typeflag = tar.TypeChar
		header.Devmajor = int64((node.Device >> 8) & 0xff)
		header.Devminor = int64(node.Device & 0xff)
	case "fifo":
		header.Typeflag = tar.TypeFifo
	case "socket":
		// TODO: skip?
	}

	err := tw.WriteHeader(header)

	if err != nil {
		return errors.Wrap(err, "TarHeader ")
	}

	return getNodeData(ctx, tw, repo, node)

}

func parseXattrs(xattrs []restic.ExtendedAttribute) map[string]string {
	tmpMap := make(map[string]string)

	for _, attr := range xattrs {
		attrString := string(attr.Value)

		if strings.HasPrefix(attr.Name, "system.posix_acl_") {
			na := acl{}
			na.decode(attr.Value)

			if na.String() != "" {
				if strings.Contains(attr.Name, "system.posix_acl_access") {
					tmpMap["SCHILY.acl.access"] = na.String()
				} else if strings.Contains(attr.Name, "system.posix_acl_default") {
					tmpMap["SCHILY.acl.default"] = na.String()
				}
			}

		} else {
			tmpMap["SCHILY.xattr."+attr.Name] = attrString
		}
	}

	return tmpMap
}
