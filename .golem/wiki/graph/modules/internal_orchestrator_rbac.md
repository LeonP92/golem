# internal/orchestrator/rbac

The rbac module defines the orchestrator's role-based access control policy as a compile-time table rather than a runtime policy engine. It declares the Role and Permission string types, the known roles (admin, developer), the six permission constants covering user management, shem registration and viewing, and ticket creation/viewing/management, and a single rolePermissions map that is the sole source of truth for which role holds which permission, denying by default for any role absent from the table. On top of that table it exposes Roles for stable display ordering in UI dropdowns and CLI messages, ParseRole for validating role strings on every write path, Can as the one seam every authorization decision flows through (nil users and unknown roles hold nothing), and Require, an HTTP middleware that must be nested inside auth.RequireSession and refuses a request with 403 when the session user in the request context lacks the required permission.

## Functions

- Roles
- ParseRole
- Can
- Require
- TestCan_Admin
- TestCan_Developer
- TestCan_UnknownRole
- TestCan_NilUser
- TestParseRole
- TestRoles_Order
- TestRequire_Permitted
- TestRequire_Forbidden
- TestRequire_NoContextUser

## Types

- Role
- Permission

## Imports

net/http, github.com/leonp92/golem/internal/orchestrator/auth, github.com/leonp92/golem/internal/orchestrator/db, net/http/httptest, testing, gorm.io/gorm, github.com/leonp92/golem/internal/orchestrator/rbac
