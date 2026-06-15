package auth

// Role is a coarse permission level. Higher roles include lower ones.
type Role string

// Roles in ascending order of privilege.
const (
	RoleObserver      Role = "observer"
	RoleOperator      Role = "operator"
	RoleAdministrator Role = "administrator"
)

var roleRank = map[Role]int{RoleObserver: 1, RoleOperator: 2, RoleAdministrator: 3}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return roleRank[r] > 0 }

// AtLeast reports whether r grants everything min grants.
func (r Role) AtLeast(min Role) bool { return roleRank[r] >= roleRank[min] }

// AllRoles lists valid roles for forms and validation.
func AllRoles() []Role { return []Role{RoleObserver, RoleOperator, RoleAdministrator} }
