package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kardianos/service"
)

// systemdScript is the unit template used when installing the service. It
// replaces the library default to run as a notify service with automatic
// restart, so systemd only considers the server started once the socket is
// bound. RuntimeDirectory gives the socket a directory systemd creates on
// start and removes on stop.
const systemdScript = `[Unit]
Description={{Description}}
ConditionFileIsExecutable={{Path | cmdEscape}}
{{range Dependencies}}{{.}}
{{end}}StartLimitIntervalSec=500
StartLimitBurst=5

[Service]
Type=notify
ExecStart={{Path | cmdEscape}}{{range Arguments}} {{. | cmd}}{{end}}
{{if ChRoot}}RootDirectory={{ChRoot | cmd}}
{{end}}{{if WorkingDirectory}}WorkingDirectory={{WorkingDirectory | cmdEscape}}
{{end}}{{if UserName}}User={{UserName}}
{{end}}{{if ReloadSignal}}ExecReload=/bin/kill -{{ReloadSignal}} "$MAINPID"
{{end}}{{if PIDFile}}PIDFile={{PIDFile | cmd}}
{{end}}{{if OutputFileSupport}}StandardOutput=file:{{LogDirectory}}/{{Name}}.out
StandardError=file:{{LogDirectory}}/{{Name}}.err
{{end}}{{if LimitNOFILE}}LimitNOFILE={{LimitNOFILE}}
{{end}}{{if Restart}}Restart={{Restart}}
{{end}}{{if SuccessExitStatus}}SuccessExitStatus={{SuccessExitStatus}}
{{end}}RuntimeDirectory={{Name}}
RestartSec=5
EnvironmentFile=-/etc/sysconfig/{{Name}}

{{range EnvVars}}{{.}}
{{end}}[Install]
WantedBy=multi-user.target
`

// ServiceAction lists the accepted service actions, in the order the Run
// switch handles them.
var ServiceAction = []string{"start", "stop", "status", "restart", "install", "uninstall"}

// ServiceCmd manages the purge server as a system service.
type ServiceCmd struct {
	Action     string   `arg:"" enum:"${serviceActions}" help:"${serviceActions}" required:""`
	CachePaths []string `name:"cache-path" type:"path" help:"Cache directory the installed service may purge, can be repeated. Any path is purgeable when none is given."`
}

// action returns the requested service action.
func (s *ServiceCmd) action() string {
	return s.Action
}

// arguments builds the command line the installed unit runs. The allowlist
// belongs in the unit rather than in a drop-in written afterwards: a service
// installed with cache paths then serves no others from its first start.
func (s *ServiceCmd) arguments() []string {
	arguments := make([]string, 0, 1+2*len(s.CachePaths))
	arguments = append(arguments, "server")
	for _, cachePath := range s.CachePaths {
		// The unit runs from a working directory of the service manager's
		// choosing, so a relative path here would name a different directory
		// than the one the install was typed against. Symlinks are left alone:
		// the server resolves them per request, and baking the target into the
		// unit would pin the allowlist to wherever the link pointed at install.
		if absolute, err := filepath.Abs(cachePath); err == nil {
			cachePath = absolute
		}
		arguments = append(arguments, "--cache-path", cachePath)
	}
	return arguments
}

// Run performs the requested action against the installed service.
func (s *ServiceCmd) Run() (err error) {
	// The allowlist is written into the unit, so it only takes effect at
	// install. Accepting it on the other actions would read as having changed
	// the allowlist of a service that carries on with the one it was installed
	// with, which is the sort of misreading that leaves a cache purgeable.
	if len(s.CachePaths) != 0 && s.action() != ServiceAction[4] {
		return fmt.Errorf("--cache-path only applies to %s; reinstall the service to change it", ServiceAction[4])
	}
	svc, err := s.service()
	if err != nil {
		return err
	}
	switch s.action() {
	case ServiceAction[0]:
		err = svc.Start()
	case ServiceAction[1]:
		err = svc.Stop()
	case ServiceAction[2]:
		var status service.Status
		status, err = svc.Status()
		if err == nil {
			switch status {
			case service.StatusRunning:
				fmt.Println("Service is running.")
			case service.StatusStopped:
				fmt.Println("Service is stopped.")
			default:
				fmt.Println("Service is in an unknown state.")
			}
		}
	case ServiceAction[3]:
		err = svc.Restart()
	case ServiceAction[4]:
		// A mistyped cache path installs cleanly and then refuses every purge
		// of the cache it was meant to name, so say so now rather than leave it
		// to be found by a 403. A cache directory nginx has not created yet is
		// the same shape, which is why this is a warning and not a failure.
		for _, cachePath := range s.CachePaths {
			if _, statErr := os.Stat(cachePath); statErr != nil {
				fmt.Printf("Warning: cache path %s cannot be read: %s\n", cachePath, statErr)
			}
		}
		err = svc.Install()
	case ServiceAction[5]:
		err = svc.Uninstall()
	}
	if err != nil {
		return err
	}
	// Status already printed its own result.
	if s.action() != ServiceAction[2] {
		fmt.Println("Command executed successfully.")
	}
	return
}

// service builds the service definition shared by the management actions and
// by the server when it is started by the service manager.
func (s *ServiceCmd) service() (service.Service, error) {
	svcConfig := &service.Config{
		Name:         Name,
		DisplayName:  DisplayName,
		Description:  Description,
		Arguments:    s.arguments(),
		Dependencies: []string{"After=network.target"},
		Option: service.KeyValue{
			"SystemdScript": systemdScript,
			"Restart":       "always",
		},
	}
	return service.New(s, svcConfig)
}

// Start satisfies service.Interface. The server is already running in the
// foreground by the time the supervisor attaches.
func (s *ServiceCmd) Start(svc service.Service) error {
	return nil
}

// Stop satisfies service.Interface, signalling the server's shutdown. The send
// cannot block: a shutdown already under way leaves nothing reading the
// channel, and holding the supervisor here would stall the stop it asked for.
func (s *ServiceCmd) Stop(svc service.Service) error {
	select {
	case stopChan <- struct{}{}:
	default:
	}
	return nil
}
