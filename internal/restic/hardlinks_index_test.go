package restic_test

import (
	"testing"

	"github.com/classmarkets/restic/internal/restic"
	rtest "github.com/classmarkets/restic/internal/test"
)

// TestHardLinks contains various tests for HardlinkIndex.
func TestHardLinks(t *testing.T) {

	idx := restic.NewHardlinkIndex()

	idx.Add(1, 2, "inode1-file1-on-device2")
	idx.Add(2, 3, "inode2-file2-on-device3")

	var sresult string
	sresult = idx.GetFilename(1, 2)
	rtest.Equals(t, sresult, "inode1-file1-on-device2")

	sresult = idx.GetFilename(2, 3)
	rtest.Equals(t, sresult, "inode2-file2-on-device3")

	var bresult bool
	bresult = idx.Has(1, 2)
	rtest.Equals(t, bresult, true)

	bresult = idx.Has(1, 3)
	rtest.Equals(t, bresult, false)

	idx.Remove(1, 2)
	bresult = idx.Has(1, 2)
	rtest.Equals(t, bresult, false)
}
