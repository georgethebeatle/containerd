package execution

import (
	"fmt"
	"os"
	"syscall"
	"time"

	api "github.com/docker/containerd/api/execution"
	"github.com/docker/containerd/events"
	google_protobuf "github.com/golang/protobuf/ptypes/empty"
	"github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/net/context"
)

var (
	emptyResponse = &google_protobuf.Empty{}
)

func New(ctx context.Context, executor Executor) (*Service, error) {
	svc := &Service{
		executor: executor,
	}

	// List existing container, some of them may have died away if
	// we've been restarted
	containers, err := executor.List(ctx)
	if err != nil {
		return nil, err
	}

	for _, c := range containers {
		status := c.Status()
		// generate exit event for all processes, (generate event for init last)
		processes := c.Processes()
		processes = append(processes[1:], processes[0])
		for _, p := range c.processes {
			if status == Stopped || status == Deleted {
				if p.Status() != Stopped {
					p.Signal(os.Kill)
				}
				sc, err := p.Wait()
				if err != nil {
					sc = UnknownStatusCode
				}
				topic := GetContainerProcessEventTopic(c.ID(), p.ID())
				svc.publishEvent(ctx, topic, &ContainerExitEvent{
					ContainerEvent: ContainerEvent{
						Timestamp: time.Now(),
						ID:        c.ID(),
						Action:    "exit",
					},
					PID:        p.ID(),
					StatusCode: sc,
				})
			} else {
				svc.monitorProcess(ctx, c, p)
			}
		}
	}

	return svc, nil
}

type Service struct {
	executor Executor
}

func (s *Service) Create(ctx context.Context, r *api.CreateContainerRequest) (*api.CreateContainerResponse, error) {
	var err error

	container, err := s.executor.Create(ctx, r.ID, CreateOpts{
		Bundle:  r.BundlePath,
		Console: r.Console,
		Stdin:   r.Stdin,
		Stdout:  r.Stdout,
		Stderr:  r.Stderr,
	})
	if err != nil {
		return nil, err
	}

	procs := container.Processes()
	initProcess := procs[0]

	s.monitorProcess(ctx, container, initProcess)

	return &api.CreateContainerResponse{
		Container:   toGRPCContainer(container),
		InitProcess: toGRPCProcess(initProcess),
	}, nil
}

func (s *Service) Delete(ctx context.Context, r *api.DeleteContainerRequest) (*google_protobuf.Empty, error) {
	container, err := s.executor.Load(ctx, r.ID)
	if err != nil {
		return emptyResponse, err
	}

	if err = s.executor.Delete(ctx, container); err != nil {
		return emptyResponse, err
	}
	return emptyResponse, nil
}

func (s *Service) List(ctx context.Context, r *api.ListContainersRequest) (*api.ListContainersResponse, error) {
	containers, err := s.executor.List(ctx)
	if err != nil {
		return nil, err
	}
	resp := &api.ListContainersResponse{}
	for _, c := range containers {
		resp.Containers = append(resp.Containers, toGRPCContainer(c))
	}
	return resp, nil
}
func (s *Service) Get(ctx context.Context, r *api.GetContainerRequest) (*api.GetContainerResponse, error) {
	container, err := s.executor.Load(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	return &api.GetContainerResponse{
		Container: toGRPCContainer(container),
	}, nil
}

func (s *Service) Update(ctx context.Context, r *api.UpdateContainerRequest) (*google_protobuf.Empty, error) {
	return emptyResponse, nil
}

func (s *Service) Pause(ctx context.Context, r *api.PauseContainerRequest) (*google_protobuf.Empty, error) {
	container, err := s.executor.Load(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	return emptyResponse, s.executor.Pause(ctx, container)
}

func (s *Service) Resume(ctx context.Context, r *api.ResumeContainerRequest) (*google_protobuf.Empty, error) {
	container, err := s.executor.Load(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	return emptyResponse, s.executor.Resume(ctx, container)
}

func (s *Service) Start(ctx context.Context, r *api.StartContainerRequest) (*google_protobuf.Empty, error) {
	container, err := s.executor.Load(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	return emptyResponse, s.executor.Start(ctx, container)
}

func (s *Service) StartProcess(ctx context.Context, r *api.StartProcessRequest) (*api.StartProcessResponse, error) {
	container, err := s.executor.Load(ctx, r.ContainerID)
	if err != nil {
		return nil, err
	}

	spec := specs.Process{
		Terminal: r.Process.Terminal,
		ConsoleSize: specs.Box{
			Height: 80,
			Width:  80,
		},
		Args:            r.Process.Args,
		Env:             r.Process.Env,
		Cwd:             r.Process.Cwd,
		NoNewPrivileges: true,
	}

	process, err := s.executor.StartProcess(ctx, container, StartProcessOpts{
		ID:      r.Process.ID,
		Spec:    spec,
		Console: r.Console,
		Stdin:   r.Stdin,
		Stdout:  r.Stdout,
		Stderr:  r.Stderr,
	})
	if err != nil {
		return nil, err
	}

	s.monitorProcess(ctx, container, process)

	return &api.StartProcessResponse{
		Process: toGRPCProcess(process),
	}, nil
}

// containerd managed execs + system pids forked in container
func (s *Service) GetProcess(ctx context.Context, r *api.GetProcessRequest) (*api.GetProcessResponse, error) {
	container, err := s.executor.Load(ctx, r.ContainerID)
	if err != nil {
		return nil, err
	}
	process := container.GetProcess(r.ProcessID)
	if process == nil {
		return nil, ErrProcessNotFound
	}
	return &api.GetProcessResponse{
		Process: toGRPCProcess(process),
	}, nil
}

func (s *Service) SignalProcess(ctx context.Context, r *api.SignalProcessRequest) (*google_protobuf.Empty, error) {
	container, err := s.executor.Load(ctx, r.ContainerID)
	if err != nil {
		return emptyResponse, err
	}
	process := container.GetProcess(r.ProcessID)
	if process == nil {
		return nil, fmt.Errorf("Make me a constant! Process not foumd!")
	}
	return emptyResponse, process.Signal(syscall.Signal(r.Signal))
}

func (s *Service) DeleteProcess(ctx context.Context, r *api.DeleteProcessRequest) (*google_protobuf.Empty, error) {
	container, err := s.executor.Load(ctx, r.ContainerID)
	if err != nil {
		return emptyResponse, err
	}
	if err := s.executor.DeleteProcess(ctx, container, r.ProcessID); err != nil {
		return emptyResponse, err
	}
	return emptyResponse, nil
}

func (s *Service) ListProcesses(ctx context.Context, r *api.ListProcessesRequest) (*api.ListProcessesResponse, error) {
	container, err := s.executor.Load(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	processes := container.Processes()
	return &api.ListProcessesResponse{
		Processes: toGRPCProcesses(processes),
	}, nil
}

var (
	_ = (api.ExecutionServiceServer)(&Service{})
)

func (s *Service) publishEvent(ctx context.Context, topic string, v interface{}) {
	ctx = events.WithTopic(ctx, topic)
	events.GetPoster(ctx).Post(ctx, v)
}

func (s *Service) monitorProcess(ctx context.Context, container *Container, process Process) {
	go func() {
		status, err := process.Wait()
		if err == nil {
			topic := GetContainerProcessEventTopic(container.ID(), process.ID())
			s.publishEvent(ctx, topic, &ContainerExitEvent{
				ContainerEvent: ContainerEvent{
					Timestamp: time.Now(),
					ID:        container.ID(),
					Action:    "exit",
				},
				PID:        process.ID(),
				StatusCode: status,
			})
		}
	}()
}

func GetContainerEventTopic(id string) string {
	return fmt.Sprintf(containerEventsTopicFormat, id)
}

func GetContainerProcessEventTopic(containerID, processID string) string {
	return fmt.Sprintf(containerProcessEventsTopicFormat, containerID, processID)
}

func toGRPCContainer(container *Container) *api.Container {
	c := &api.Container{
		ID:         container.ID(),
		BundlePath: container.Bundle(),
	}
	status := container.Status()
	switch status {
	case "created":
		c.Status = api.Status_CREATED
	case "running":
		c.Status = api.Status_RUNNING
	case "stopped":
		c.Status = api.Status_STOPPED
	case "paused":
		c.Status = api.Status_PAUSED
	}

	return c
}

func toGRPCProcesses(processes []Process) []*api.Process {
	var out []*api.Process
	for _, p := range processes {
		out = append(out, toGRPCProcess(p))
	}
	return out
}

func toGRPCProcess(process Process) *api.Process {
	return &api.Process{
		ID:  process.ID(),
		Pid: process.Pid(),
	}
}
