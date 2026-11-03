package engine

import (
	"fmt"

	"github.com/subuk/richt/config"
)

func (s *configSequence) ownership(owner, group, state string) (config.Ownership, error) {
	var out config.Ownership
	if state == "absent" {
		return out, nil
	}
	if id, exists, known := s.accounts.User(owner); owner != "" && known {
		if !exists {
			return out, fmt.Errorf("owner %q follows its removal", owner)
		}
		out.OwnerID, out.OwnerPending = id, id == nil
	}
	if id, exists, known := s.accounts.Group(group); group != "" && known {
		if !exists {
			return out, fmt.Errorf("group %q follows its removal", group)
		}
		out.GroupID, out.GroupPending = id, id == nil
	}
	return out, nil
}

// Explicit dependencies do not reorder configs or select prerequisites. Check
// declared identities before target lookup, even if a later identity currently
// exists on the target: the same inventory must also work on a clean host.
func checkAccountOrder(services []preparedService) error {
	type declaration struct {
		position         int
		state, component string
	}
	declared := map[string]declaration{}
	position := 0
	for _, service := range services {
		for _, f := range service.files {
			if c := f.definition; c != nil {
				name := ""
				if c.Type == "user" {
					name = c.User
				}
				if c.Type == "group" {
					name = c.Group
				}
				if name != "" {
					declared[c.Type+":"+name] = declaration{position, service.service.State, service.service.Name + "/" + f.component}
				}
			}
			position++
		}
	}
	position = 0
	for _, service := range services {
		for _, f := range service.files {
			component := service.service.Name + "/" + f.component
			check := func(kind, name string) error {
				if name == "" {
					return nil
				}
				d, ok := declared[kind+":"+name]
				if !ok {
					return nil
				}
				if service.service.State != "absent" && d.state == "absent" {
					return fmt.Errorf("%s requires %s %q managed by %s, but that account is declared absent", component, kind, name, d.component)
				}
				if service.service.State != "absent" && d.state != "absent" && d.position >= position {
					return fmt.Errorf("%s precedes %s %q managed by %s; declare the account before its consumers", component, kind, name, d.component)
				}
				if service.service.State == "absent" && d.state == "absent" && d.position < position {
					return fmt.Errorf("%s follows removal of %s %q managed by %s; remove consumers before their accounts", component, kind, name, d.component)
				}
				return nil
			}
			if f.file != nil {
				if err := check("user", f.file.Owner); err != nil {
					return err
				}
				if err := check("group", f.file.Group); err != nil {
					return err
				}
			} else if c := f.definition; c != nil {
				switch c.Type {
				case "user":
					if err := check("group", c.Group); err != nil {
						return err
					}
					if c.SupplementaryGroups != nil {
						for _, group := range *c.SupplementaryGroups {
							if err := check("group", group); err != nil {
								return err
							}
						}
					}
				case "file", "directory":
					if err := check("user", c.Owner); err != nil {
						return err
					}
					if err := check("group", c.Group); err != nil {
						return err
					}
				}
			}
			position++
		}
	}
	return nil
}
