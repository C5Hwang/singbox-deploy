package system

// basePackages are the dependencies installed before sing-box and Nginx setup.
// Nginx itself is installed from the nginx.org mainline repo in a later step.
var basePackages = []string{"curl", "wget", "tar", "unzip", "openssl", "ca-certificates"}

var aptNoninteractiveEnv = []string{
	"DEBIAN_FRONTEND=noninteractive",
	"APT_LISTCHANGES_FRONTEND=none",
	"NEEDRESTART_MODE=a",
}

var aptInstallOptions = []string{
	"-y",
	"-o", "Dpkg::Options::=--force-confdef",
	"-o", "Dpkg::Options::=--force-confold",
}

// InstallPlan is an ordered list of commands to run during base dependency
// installation.
type InstallPlan struct {
	Commands []Command
}

// BuildInstallPlan returns the base-dependency install plan for the package
// manager. apt runs an update first; dnf/yum install directly.
func BuildInstallPlan(osr OSRelease) InstallPlan {
	switch osr.PackageManager {
	case "apt":
		installArgs := append([]string{}, aptInstallOptions...)
		installArgs = append(installArgs, "install")
		installArgs = append(installArgs, basePackages...)
		return InstallPlan{Commands: []Command{
			{Name: "apt-get", Args: []string{"update"}, Env: aptNoninteractiveEnv},
			{Name: "apt-get", Args: installArgs, Env: aptNoninteractiveEnv},
		}}
	case "dnf", "yum":
		return InstallPlan{Commands: []Command{
			{Name: osr.PackageManager, Args: append([]string{"-y", "install"}, basePackages...)},
		}}
	default:
		return InstallPlan{}
	}
}

// RunInstallPlan executes every command in the plan in order, stopping at the
// first error.
func RunInstallPlan(r Runner, p InstallPlan) error {
	for _, cmd := range p.Commands {
		if err := r.Run(cmd); err != nil {
			return err
		}
	}
	return nil
}
