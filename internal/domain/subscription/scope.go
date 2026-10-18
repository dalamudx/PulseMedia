package subscription

import (
	"github.com/google/uuid"
)

type Scope struct {
	Type            string
	Seasons         []int
	IncludeSpecials bool
	Selected        []uuid.UUID
}
type ScopeUnit struct {
	ID      uuid.UUID
	Season  int
	Retired bool
	TV      bool
}

func ScopeContains(scope Scope, u ScopeUnit) bool {
	if u.Retired {
		return false
	}
	if u.TV {
		if u.Season == 0 && !scope.IncludeSpecials {
			return false
		}
		if len(scope.Seasons) > 0 {
			found := false
			for _, s := range scope.Seasons {
				if s == u.Season {
					found = true
				}
			}
			if !found {
				return false
			}
		}
	}
	if scope.Type == "all" {
		return true
	}
	if scope.Type != "selected" {
		return false
	}
	for _, id := range scope.Selected {
		if id == u.ID {
			return true
		}
	}
	return false
}
