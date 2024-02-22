package biscepter

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
)

type jobConfig struct {
	Repository string `yaml:"repository"`

	GoodCommit string `yaml:"goodCommit"`
	BadCommit  string `yaml:"badCommit"`

	ErrorExitCode int `yaml:"errorExitCode"`

	Port  int   `yaml:"port"`
	Ports []int `yaml:"ports"`

	Healthcheck []healthcheckConf `yaml:"healthcheck"`

	Dockerfile             string `yaml:"dockerfile"`
	DockerfilePath         string `yaml:"dockerfilePath"`
	DockerfilePathRelative string `yaml:"dockerfilePathRelative"`

	BuildCost float64 `yaml:"buildCost"`
}

// GetJobFromConfig reads in a job config in yaml format from a reader and initializes the corresponding job struct
func GetJobFromConfig(r io.Reader) (*Job, error) {
	var config jobConfig

	// Read in yaml
	decoder := yaml.NewDecoder(r)
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}

	// Convert to Job struct
	job := Job{
		BuildCost: config.BuildCost,

		Dockerfile:             config.Dockerfile,
		DockerfilePath:         config.DockerfilePath,
		DockerfilePathRelative: config.DockerfilePathRelative,

		repository: config.Repository,
	}

	job.Ports = config.Ports
	if config.Port != 0 {
		job.Ports = []int{config.Port}
	}

	for _, check := range config.Healthcheck {
		// TODO: Implement fully
		job.Healthchecks = append(job.Healthchecks, Healthcheck{
			Port: check.Port,
		})
	}

	return &job, nil
}

// A job represents a blueprint for replicas, which are then used to bisect one issue.
// Jobs can create multiple replicas at once.
type Job struct {
	ReplicasCount int // How many replicas of itself this job should spawn simultaneously. Each replica is to be used for bisecting one issue.

	BuildCost float64 // TODO: Explain this precisely

	Ports        []int         // The ports which this job needs
	Healthchecks []Healthcheck // The healthchecks for this job

	// TODO: Docs
	GoodCommit string
	BadCommit  string

	Dockerfile             string // The contents of the dockerfile.
	DockerfilePath         string // The path to the dockerfile relative to the present working directory. Only gets used if Dockerfile is empty.
	DockerfilePathRelative string // The path to the dockerfile relative to the project's root. Only gets used if Dockerfile and DockerfilePath are empty.

	replicas []*replica // This job's replicas

	repository string // The repository URL
	repoPath   string // The path to the original cloned repository which replicas will copy from

	commits []string // This job's commits, where commits[0] is the bad commit and commits[N-1] is the good commit
}

// Run the job. This initializes all the replicas and starts them. This function returns a [RunningSystem] channel and an [OffendingCommit] channel.
// The [RunningSystem] channel should be used to get notified about systems which are ready to be tested.
// Once an [OffendingCommit] was received for a given replica index, no more [RunningSystem] structs for this replica will appear in the [RunningSystem] channel.
func (job *Job) Run() (chan RunningSystem, chan OffendingCommit, error) {
	// Clone repo
	var err error
	job.repoPath, err = os.MkdirTemp("", "")
	if err != nil {
		return nil, nil, err
	}
	if err := exec.Command("git", "clone", job.repository, job.repoPath).Run(); err != nil {
		return nil, nil, err
	}

	// Get all commits
	cmd := exec.Command("git", "rev-list", "--first-parent", "^"+job.GoodCommit, job.BadCommit)
	cmd.Dir = job.repoPath
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, err
	}
	job.commits = strings.Split(string(out), "\n")

	fmt.Printf("Commits: %s\n", job.commits)

	// Make the channels
	// TODO: Don't hardcode channel size
	rsChan, ocChan := make(chan RunningSystem, 100), make(chan OffendingCommit, 100)

	job.replicas = make([]*replica, job.ReplicasCount)

	// Create all replicas
	for i := range job.ReplicasCount {
		var err error
		// Create a new replica
		job.replicas[i], err = createJobReplica(job, i)
		if err != nil {
			// Stop running replicas
			for j := range i {
				if err := job.replicas[j].stop(); err != nil {
					return nil, nil, err
				}
			}
			return nil, nil, err
		}

		// Start the created replica
		if err = job.replicas[i].start(rsChan, ocChan); err != nil {
			// Stop running replicas
			for j := range i {
				if err := job.replicas[j].stop(); err != nil {
					return nil, nil, err
				}
			}
			return nil, nil, err
		}
	}

	return rsChan, ocChan, nil
}

// Stop the job and all running replicas.
func (j *Job) Stop() error {
	for _, replica := range j.replicas {
		if err := replica.stop(); err != nil {
			return err
		}
	}

	return os.RemoveAll(j.repoPath)
}
