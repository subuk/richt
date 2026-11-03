package config

import (
	"strconv"

	"github.com/subuk/richt/plan"
)

// Ownership supplies identities established by preceding account configs during
// preparation. Pending identities will be allocated at apply, so ownership must
// be applied without comparing a numeric ID. These values never enter the plan.
type Ownership struct {
	OwnerID, GroupID           *uint32
	OwnerPending, GroupPending bool
}

func projectedOwnership(values []Ownership) Ownership {
	if len(values) != 0 {
		return values[0]
	}
	return Ownership{}
}

func (o Ownership) pending() bool { return o.OwnerPending || o.GroupPending }

func (o Ownership) observedNames(owner, group string) (string, string) {
	if o.OwnerID != nil || o.OwnerPending {
		owner = ""
	}
	if o.GroupID != nil || o.GroupPending {
		group = ""
	}
	return owner, group
}

func (o Ownership) local(f *plan.FileChange) (int, int, error) {
	owner, group := o.observedNames(f.Owner, f.Group)
	uid, gid, err := ownership(&plan.FileChange{Owner: owner, Group: group})
	if o.OwnerID != nil {
		uid = int(*o.OwnerID)
	}
	if o.GroupID != nil {
		gid = int(*o.GroupID)
	}
	return uid, gid, err
}

func (o Ownership) remote(owner, group string) (string, string) {
	if o.OwnerID != nil {
		owner = strconv.FormatUint(uint64(*o.OwnerID), 10)
	}
	if o.GroupID != nil {
		group = strconv.FormatUint(uint64(*o.GroupID), 10)
	}
	return owner, group
}
