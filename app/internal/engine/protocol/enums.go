package protocol

import "regexp"

// Service is a compose service managed by the agent.
type Service string

const (
	ServiceDB           Service = "db"
	ServiceRedis        Service = "redis"
	ServiceMinio        Service = "minio"
	ServiceBackend      Service = "backend"
	ServiceCeleryWorker Service = "celery-worker"
	ServiceCeleryBeat   Service = "celery-beat"
	ServiceFrontend     Service = "frontend"
	ServiceCaddy        Service = "caddy"
	ServiceBackup       Service = "backup"
	ServiceMigrate      Service = "migrate"
)

// Services lists every valid Service.
var Services = []Service{
	ServiceDB, ServiceRedis, ServiceMinio, ServiceBackend, ServiceCeleryWorker,
	ServiceCeleryBeat, ServiceFrontend, ServiceCaddy, ServiceBackup, ServiceMigrate,
}

func (s Service) Valid() bool { return contains(Services, s) }

// ImageRole identifies which image slot an image fills.
type ImageRole string

const (
	ImageRoleBackend  ImageRole = "backend"
	ImageRoleFrontend ImageRole = "frontend"
	ImageRoleCaddy    ImageRole = "caddy"
	ImageRoleBackup   ImageRole = "backup"
	ImageRolePostgres ImageRole = "postgres"
	ImageRoleRedis    ImageRole = "redis"
	ImageRoleSilo     ImageRole = "silo"

	// Builder images: the toolchain stages of the CARE images, used by
	// image.build to derive plugin-enabled or rebuilt images in the VM.
	// They are not stack slots, so they are not in ImageRoles.
	ImageRoleBackendBuilder  ImageRole = "backend-builder"
	ImageRoleFrontendBuilder ImageRole = "frontend-builder"
)

// ImageRoles lists every valid ImageRole.
var ImageRoles = []ImageRole{
	ImageRoleBackend, ImageRoleFrontend, ImageRoleCaddy, ImageRoleBackup,
	ImageRolePostgres, ImageRoleRedis, ImageRoleSilo,
}

// BuilderRoles lists the builder image roles.
var BuilderRoles = []ImageRole{ImageRoleBackendBuilder, ImageRoleFrontendBuilder}

func (r ImageRole) Valid() bool { return contains(ImageRoles, r) || contains(BuilderRoles, r) }

// BuildKind selects which derived image the agent builds.
type BuildKind string

const (
	BuildBackendPlugins  BuildKind = "backend-plugins"
	BuildFrontendRebuild BuildKind = "frontend-rebuild"
)

// BuildKinds lists every valid BuildKind.
var BuildKinds = []BuildKind{BuildBackendPlugins, BuildFrontendRebuild}

func (k BuildKind) Valid() bool { return contains(BuildKinds, k) }

// ManageCommand is an allow-listed Django management command.
type ManageCommand string

const (
	ManageCreateSuperuser      ManageCommand = "createsuperuser"
	ManageChangePassword       ManageCommand = "changepassword"
	ManageSyncPermissionsRoles ManageCommand = "sync_permissions_roles"
	ManageSyncValueset         ManageCommand = "sync_valueset"
)

// ManageCommands lists every valid ManageCommand.
var ManageCommands = []ManageCommand{
	ManageCreateSuperuser, ManageChangePassword, ManageSyncPermissionsRoles, ManageSyncValueset,
}

func (c ManageCommand) Valid() bool { return contains(ManageCommands, c) }

// needsCredentials reports whether the command takes a username and password file.
func (c ManageCommand) needsCredentials() bool {
	return c == ManageCreateSuperuser || c == ManageChangePassword
}

// FileArea is an agent-owned directory that files may be uploaded into.
type FileArea string

const (
	FileAreaConfig       FileArea = "config"
	FileAreaBuild        FileArea = "build"
	FileAreaRestoreInput FileArea = "restore-input"
)

// FileAreas lists every valid FileArea.
var FileAreas = []FileArea{FileAreaConfig, FileAreaBuild, FileAreaRestoreInput}

func (a FileArea) Valid() bool { return contains(FileAreas, a) }

// Digest is an OCI content digest, e.g. "sha256:<64 hex>".
type Digest string

var digestRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func (d Digest) Valid() bool { return digestRE.MatchString(string(d)) }

func contains[T comparable](set []T, v T) bool {
	for _, x := range set {
		if x == v {
			return true
		}
	}
	return false
}
