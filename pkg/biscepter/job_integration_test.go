//go:build integration

package biscepter_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CelineWuest/biscepter/pkg/biscepter"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func bisectTestRepo(t *testing.T, replicas int, endpointOffset int, goodCommit, badCommit string, expectedCommits []string) {
	job := biscepter.Job{
		Log:           logrus.StandardLogger(),
		ReplicasCount: replicas,

		Ports: []int{3333},

		Healthchecks: []biscepter.Healthcheck{
			{Port: 3333, CheckType: biscepter.HttpGet200, Data: "/1", Config: biscepter.HealthcheckConfig{Retries: 50, Backoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond}},
		},

		CommitReplacementsBackup: "/dev/null",

		GoodCommit: goodCommit,
		BadCommit:  badCommit,

		Dockerfile: `
FROM golang:1.22.0-alpine
WORKDIR /app
COPY . .
RUN go build -o server main.go
CMD ./server
`,

		Repository: "https://github.com/CelineWuest/biscepter-test-repo.git",
	}

	job.Log.SetLevel(logrus.TraceLevel)
	job.Log.SetOutput(os.Stdout)

	// Running the job
	rsChan, ocChan, err := job.Run()
	if err != nil {
		panic(err)
	}

	// Waiting for running systems or offending commit
	offendingCommits := 0
	for {
		select {
		// Offending commit found
		case commit := <-ocChan:
			if commit.ReplicaIndex < 0 || commit.ReplicaIndex >= replicas {
				assert.FailNowf(t, "Failed to get offending commit", "Got bogus replica index: %d", commit.ReplicaIndex)
			}

			assert.Equal(t, expectedCommits[commit.ReplicaIndex], commit.Commit, "Bisection returned wrong commit")

			offendingCommits++
			if offendingCommits == replicas {
				err := job.Stop()
				assert.Nil(t, err, "Failed to stop job")
				return
			}
		case system := <-rsChan:
			res, err := http.Get(fmt.Sprintf("http://localhost:%d/%d", system.Ports[3333], system.ReplicaIndex+endpointOffset))
			assert.Nil(t, err, "Failed to get response from webserver")

			resBytes, err := io.ReadAll(res.Body)
			assert.Nil(t, err, "Failed to read response body")
			resText := string(resBytes)

			if resText == fmt.Sprint(system.ReplicaIndex+endpointOffset) {
				system.IsGood()
			} else {
				system.IsBad()
			}
		}
	}
}

// cleanupDocker returns a function which deletes any container and image whose tag is the one passed to this function
func cleanupDocker(tag string) func() {
	return func() {

		// Cleanup images and containers
		cli, _ := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		defer cli.Close()

		// Clean containers
		containers, _ := cli.ContainerList(context.Background(), container.ListOptions{
			All: true,
		})
		for _, c := range containers {
			if strings.HasSuffix(c.Image, tag) {
				cli.ContainerRemove(context.Background(), c.ID, container.RemoveOptions{Force: true})
			}
		}

		// Clean images
		images, _ := cli.ImageList(context.Background(), image.ListOptions{
			All: true,
			Filters: filters.NewArgs(
				filters.KeyValuePair{
					Key:   "reference",
					Value: "biscepter-*" + tag,
				},
			),
		})
		for _, i := range images {
			cli.ImageRemove(context.Background(), i.ID, image.RemoveOptions{
				PruneChildren: true,
				Force:         true,
			})
		}
	}
}

func TestIntegration(t *testing.T) {
	t.Run("Bisecting Single Issue", func(t *testing.T) {
		bisectTestRepo(t,
			1,
			0,
			"69931611d894fc64d1caa98ad819d44d446a23c4",
			"130f2278ef36a4690ea92781d63879ba2453ecac",
			[]string{
				"fc948eda71818bf9493735151fa0acedb4328024",
			},
		)
	})

	t.Run("Bisecting Multiple Issues", func(t *testing.T) {
		bisectTestRepo(t,
			3,
			0,
			"69931611d894fc64d1caa98ad819d44d446a23c4",
			"3219afdce02b9eb2633343ba6abe4e77b114b130",
			[]string{
				"fc948eda71818bf9493735151fa0acedb4328024",
				"d7b2ed805547dec1010e945c6f1958407a694e57",
				"130f2278ef36a4690ea92781d63879ba2453ecac",
			},
		)
	})

	t.Run("Bisecting Merges", func(t *testing.T) {
		bisectTestRepo(t,
			3,
			3,
			"0fe06fb660b5351832a71dbfe7be48bb6b69bdd0",
			"41b75b611b89b40009676fc87d3e54ae60270030",
			[]string{
				"bd7c3fdb354d9c230c4b5c655e8e7b7435f93dd8",
				"8ea445c4f48669df1e00080c4237d5961f1f41d9",
				"c8f90f669682496ca30380dc6660ab08fccac00e",
			},
		)
	})

	t.Cleanup(cleanupDocker(":13459bf98084bed7c4144d7abdbabb2367585b06136ef2d713a75a4423234656"))
}

func TestReplacingBrokenCommits(t *testing.T) {
	t.Parallel()

	replacements, err := os.CreateTemp("", "")
	assert.NoError(t, err, "Failed to create tmp file")

	job := biscepter.Job{
		Log:           logrus.StandardLogger(),
		ReplicasCount: 1,

		Ports: []int{3333},

		Healthchecks: []biscepter.Healthcheck{
			{Port: 3333, CheckType: biscepter.HttpGet200, Data: "/1", Config: biscepter.HealthcheckConfig{Retries: 50, Backoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond}},
		},

		GoodCommit: "69931611d894fc64d1caa98ad819d44d446a23c4",
		BadCommit:  "130f2278ef36a4690ea92781d63879ba2453ecac",

		CommitReplacementsBackup: replacements.Name(),

		Dockerfile: `
FROM golang:1.22.0-alpine
WORKDIR /app
RUN apk add git
COPY . .
RUN [[ $(git rev-parse HEAD) != "fc948eda71818bf9493735151fa0acedb4328024" ]]
RUN go build -o server main.go
CMD ./server
`,

		Repository: "https://github.com/CelineWuest/biscepter-test-repo.git",
	}

	// Run job whose build fails on commit fc948eda71818bf9493735151fa0acedb4328024, which is the first commit to be tested
	rsChan, _, err := job.Run()
	assert.NoError(t, err, "Failed to start job")

	// Block until the first container is ready
	rs := <-rsChan

	// Make sure the commit replacement is set correctly
	out, err := io.ReadAll(replacements)
	assert.NoError(t, err, "Couldn't read replacements file")
	assert.Equal(t, "fc948eda71818bf9493735151fa0acedb4328024:d7b2ed805547dec1010e945c6f1958407a694e57,", string(out), "Commit replacement set incorrectly")

	// Report the next commit to be broken as well
	rs.IsBroken()

	// Block until next container is ready
	<-rsChan

	// Make sure the commit replacement is set correctly
	replacements.Seek(0, io.SeekStart)
	out, err = io.ReadAll(replacements)
	assert.NoError(t, err, "Couldn't read replacements file")
	assert.Equal(t, "fc948eda71818bf9493735151fa0acedb4328024:d7b2ed805547dec1010e945c6f1958407a694e57,d7b2ed805547dec1010e945c6f1958407a694e57:130f2278ef36a4690ea92781d63879ba2453ecac,", string(out), "Commit replacement set incorrectly")

	os.Remove(replacements.Name())

	job.Stop()

	cleanupDocker(":93e3bf8b4be27be133c0d4740e936aa19e2aa52fff5e96f418669eb28ac8616b")()
}

func TestPossibleOtherCommits(t *testing.T) {
	t.Parallel()
	t.Cleanup(cleanupDocker("b7815cf3a73d66569823dee6249cce911da8a4cc00ee89080eeb6a4c19fba521"))

	replacements, err := os.CreateTemp("", "")
	assert.NoError(t, err, "Failed to create tmp file")

	job := biscepter.Job{
		Log:           logrus.StandardLogger(),
		ReplicasCount: 1,

		Ports: []int{3333},

		Healthchecks: []biscepter.Healthcheck{
			{Port: 3333, CheckType: biscepter.HttpGet200, Data: "/1", Config: biscepter.HealthcheckConfig{Retries: 50, Backoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond}},
		},

		GoodCommit: "69931611d894fc64d1caa98ad819d44d446a23c4",
		BadCommit:  "3219afdce02b9eb2633343ba6abe4e77b114b130",

		CommitReplacementsBackup: replacements.Name(),

		Dockerfile: `
FROM golang:1.22.0-alpine
WORKDIR /app
RUN apk add git
COPY . .
RUN [[ $(git rev-parse HEAD) != "130f2278ef36a4690ea92781d63879ba2453ecac" ]]
RUN go build -o server main.go
CMD ./server
`,

		Repository: "https://github.com/CelineWuest/biscepter-test-repo.git",
	}

	// Run job whose build fails on commit 130f2278ef36a4690ea92781d63879ba2453ecac, which introduces a bug to /2
	rsChan, ocChan, err := job.Run()
	assert.NoError(t, err, "Failed to start job")

	// Bisect
	commitFound := false
	for !commitFound {
		select {
		case rs := <-rsChan:
			res, err := http.Get(fmt.Sprintf("http://localhost:%d/2", rs.Ports[3333]))
			assert.NoError(t, err, "Failed to get response from webserver")

			resBytes, err := io.ReadAll(res.Body)
			assert.NoError(t, err, "Failed to read response body")
			resText := string(resBytes)

			if resText == "2" {
				rs.IsGood()
			} else {
				rs.IsBad()
			}
		case oc := <-ocChan:
			assert.Equal(t, "266cef4ee0ddfe01b9bbfc5152855d5b90291a4e", oc.Commit, "Wrong commit returned as offending")
			if assert.Len(t, oc.PossibleOtherCommits, 1, "Not exactly one possible other commit returned") {
				assert.Equal(t, "130f2278ef36a4690ea92781d63879ba2453ecac", oc.PossibleOtherCommits[0], "Actual offending commit not in possible other commits")
			}
			commitFound = true
		}
	}

	assert.NoError(t, os.Remove(replacements.Name()), "Couldn't remove temp file")

	assert.NoError(t, job.Stop(), "Couldn't stop job")
}

func TestReplacingBrokenHealthcheck(t *testing.T) {
	t.Parallel()

	replacements, err := os.CreateTemp("", "")
	assert.NoError(t, err, "Failed to create tmp file")

	job := biscepter.Job{
		Log:           logrus.StandardLogger(),
		ReplicasCount: 1,

		Ports: []int{3333},

		Healthchecks: []biscepter.Healthcheck{
			{Port: 3333, CheckType: biscepter.HttpGet200, Data: "/1", Config: biscepter.HealthcheckConfig{Retries: 50, Backoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond}},
		},

		GoodCommit: "69931611d894fc64d1caa98ad819d44d446a23c4",
		BadCommit:  "130f2278ef36a4690ea92781d63879ba2453ecac",

		CommitReplacementsBackup: replacements.Name(),

		Dockerfile: `
FROM golang:1.22.0-alpine
WORKDIR /app
RUN apk add git
COPY . .
RUN go build -o server main.go
CMD [[ $(git rev-parse HEAD) != "fc948eda71818bf9493735151fa0acedb4328024" ]] && ./server
`,

		Repository: "https://github.com/CelineWuest/biscepter-test-repo.git",
	}

	// Run job whose CMD fails on commit fc948eda71818bf9493735151fa0acedb4328024, which is the first commit to be tested
	// This means the build will succeed but the healthcheck won't
	rsChan, _, err := job.Run()
	assert.NoError(t, err, "Failed to start job")

	// Block until the first container is ready
	<-rsChan

	// Make sure the commit replacement is set correctly
	out, err := io.ReadAll(replacements)
	assert.Equal(t, "fc948eda71818bf9493735151fa0acedb4328024:d7b2ed805547dec1010e945c6f1958407a694e57,", string(out), "Commit replacement set incorrectly")

	os.Remove(replacements.Name())
	job.Stop()
	cleanupDocker(":00b975cbd39dbd1f1fb2010a7015792206dd562755262667a8c98d4f33427388")()
}

func TestRunCommitByOffset(t *testing.T) {
	job := biscepter.Job{
		Log:           logrus.StandardLogger(),
		ReplicasCount: 0,

		Ports: []int{3333},

		Healthchecks: []biscepter.Healthcheck{
			{CheckType: biscepter.Script, Port: 3333, Data: "true", Config: biscepter.HealthcheckConfig{Retries: 50, Backoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond}},
		},

		CommitReplacementsBackup: "/dev/null",

		GoodCommit: "69931611d894fc64d1caa98ad819d44d446a23c4",
		BadCommit:  "130f2278ef36a4690ea92781d63879ba2453ecac",

		Dockerfile: `FROM golang:1.22.0-alpine`,

		Repository: "https://github.com/CelineWuest/biscepter-test-repo.git",
	}

	_, err := job.RunCommitByOffset(1)
	assert.Error(t, err, "Valid commit didn't raise an error on an unitialized job")

	_, _, err = job.Run()
	assert.NoError(t, err, "Job failed to initialize")

	t.Run("Negative commit offset errors", func(t *testing.T) {
		t.Parallel()
		_, err := job.RunCommitByOffset(-1)
		assert.Error(t, err, "Negative commit offset didn't raise an error")
	})

	t.Run("Out of bounds commit offset errors", func(t *testing.T) {
		t.Parallel()
		_, err := job.RunCommitByOffset(100)
		assert.Error(t, err, "Commit offset that is out of bounds didn't raise an error")
	})

	t.Run("Valid commit offset doesn't error", func(t *testing.T) {
		t.Parallel()
		rs, err := job.RunCommitByOffset(1)
		assert.NoError(t, err, "Valid commit offset caused an error")

		rs.Done()
	})

	t.Cleanup(cleanupDocker(":b306a8132f4a6eaf8f97a8f383ca81d64776f6f0b112cfab96789b54908043a9"))
}
