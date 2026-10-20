package app

import "strings"

// Explicit targets allow managers to work in another employee's or department's space.
func (s *Server) resolveSpace(u *User, scope, owner, dept string) (string, string, string, error) {
	if scope == "personal" && owner != "" && owner != u.ID {
		if !operations(u) {
			return "", "", "", forbidden()
		}
		var n int
		if e := s.DB.QueryRow("SELECT COUNT(*) FROM users WHERE id=?", owner).Scan(&n); e != nil {
			return "", "", "", e
		}
		if n != 1 {
			return "", "", "", notfound()
		}
		return "personal:" + owner, "", owner, nil
	}
	if scope == "department" && dept != "" && dept != u.DeptID {
		if !operations(u) {
			return "", "", "", forbidden()
		}
		var n int
		if e := s.DB.QueryRow("SELECT COUNT(*) FROM departments WHERE id=?", dept).Scan(&n); e != nil {
			return "", "", "", e
		}
		if n != 1 {
			return "", "", "", notfound()
		}
		return "department:" + dept, dept, u.ID, nil
	}
	if scope == "department" && u.DeptID == "" && operations(u) {
		return "department:", "", u.ID, nil
	}
	key, d, e := space(u, scope)
	return key, d, u.ID, e
}
func uploadSpaceAccessible(u *User, v *Upload) bool {
	if operations(u) {
		return true
	}
	key, _, e := space(u, v.Scope)
	return e == nil && key == v.SpaceKey
}
func uploadOwner(v *Upload) string {
	if v.Scope == "personal" {
		return strings.TrimPrefix(v.SpaceKey, "personal:")
	}
	return v.UserID
}
