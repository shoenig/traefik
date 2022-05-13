package integration

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-check/check"
	"github.com/hashicorp/nomad/api"
	"github.com/traefik/traefik/v2/integration/try"
	"github.com/traefik/traefik/v2/pkg/provider/nomad"
)

type NomadSuite struct {
	BaseSuite
	nomadClient *api.Client
	nomadURL    string

	command *exec.Cmd
	output  *bytes.Buffer
}

func (ns *NomadSuite) nomadCmd() (*exec.Cmd, *bytes.Buffer) {
	cmd := exec.Command("nomad", "agent", "-dev")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	return cmd, &out
}

func (ns *NomadSuite) SetUpSuite(c *check.C) {
	var err error

	ns.command, ns.output = ns.nomadCmd()
	err = ns.command.Start()
	c.Check(err, check.IsNil)

	ns.nomadURL = "http://" + net.JoinHostPort(ns.getComposeServiceIP(c, "nomad"), "4646")
	fmt.Println("nomadURL:", ns.nomadURL)

	ns.nomadClient, err = api.NewClient(&api.Config{
		Address: ns.nomadURL,
	})
	c.Check(err, check.IsNil)

	// wait for nomad to elect itself
	err = ns.waitForLeader()
	c.Assert(err, check.IsNil)
}

func (ns *NomadSuite) TearDownSuite(c *check.C) {
	fmt.Println("Teardown suite")
	err := ns.command.Process.Kill()
	c.Check(err, check.IsNil)
	fmt.Println("nomad command output ...")
	fmt.Println(ns.output.String())
}

func (ns *NomadSuite) waitForLeader() error {
	return try.Do(15*time.Second, func() error {
		leader, err := ns.nomadClient.Status().Leader()
		if err != nil || len(leader) == 0 {
			return fmt.Errorf("leader not found. %w", err)
		}
		return nil
	})
}

func (ns *NomadSuite) run(job string) error {
	j, parseErr := ns.nomadClient.Jobs().ParseHCL(job, true)
	if parseErr != nil {
		return parseErr
	}
	resp, _, regErr := ns.nomadClient.Jobs().Register(j, &api.WriteOptions{Region: "global"})
	if regErr != nil {
		return regErr
	}
	return try.Do(15*time.Second, func() error {
		info, _, infErr := ns.nomadClient.Evaluations().Info(resp.EvalID, &api.QueryOptions{Region: "global"})
		if infErr != nil {
			return infErr
		}
		if info.Status != "complete" {
			return fmt.Errorf("evaluation not yet complete")
		}
		return nil
	})
}

type tmplobj struct {
	NomadAddress string
	DefaultRule  string
}

func remove(path string) {
	_ = os.Remove(path)
}

func (ns *NomadSuite) TestSocatTCP(c *check.C) {
	fmt.Println("TestSocatTCP")
	var err error
	job := newJob("bash", []string{"-c", "/usr/bin/socat -v tcp-l:1234,fork exec:'echo alice'"}, []string{"treafik.enable=true"})
	fmt.Println("job")
	fmt.Println(job)
	err = ns.run(job)
	c.Assert(err, check.IsNil)

	obj := tmplobj{
		NomadAddress: ns.nomadURL,
		DefaultRule:  nomad.DefaultTemplateRule,
	}

	file := ns.adaptFile(c, "fixtures/nomad/simple.toml", obj)
	defer remove(file)

	cmd, display := ns.traefikCmd(withConfigFile(file))
	defer display(c)
	err = cmd.Start()
	c.Assert(err, check.IsNil)
	defer ns.killCmd(cmd)

	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8000/", nil)
	c.Assert(err, check.IsNil)
	request.Host = "echo"

	err = try.Request(request, 10*time.Second,
		try.StatusCodeIs(200),
		try.BodyContains("alice"),
	)
	c.Assert(err, check.IsNil)
}

func quotes(s []string) {
	for i := 0; i < len(s); i++ {
		s[i] = fmt.Sprintf("%q", s[i])
	}
}

func newJob(cmd string, args, tags []string) string {
	quotes(args)
	quotes(tags)
	job := strings.Replace(nJob, "TAGS", strings.Join(tags, ", "), 1)
	job = strings.Replace(job, "ARGS", strings.Join(args, ", "), 1)
	job = strings.Replace(job, "CMD", fmt.Sprintf("%q", cmd), 1)
	return job
}

const nJob = ` 
job "example" {
  datacenters = ["dc1"]
  type        = "service"

  group "group" {
    network {
      mode = "host"
      port "listen" {
        static = 1234
      }
    }

    service {
      name = "echo"
      provider = "nomad"
      tags = [TAGS]
    }

    task "task" {
      driver = "raw_exec"

      config {
        command = CMD
        args    = [ARGS]
        no_cgroups = true
      }

      resources {
        cpu = 10
        memory = 128
      }
    }
  }
}`
