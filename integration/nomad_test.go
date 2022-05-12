package integration

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/go-check/check"
	"github.com/hashicorp/nomad/api"
	"github.com/traefik/traefik/v2/integration/try"
)

type NomadSuite struct {
	BaseSuite
	nomadClient *api.Client
	nomadURL    string
}

func (ns *NomadSuite) SetUpSuite(c *check.C) {
	ns.createComposeProject(c, "nomad")
	ns.composeUp(c)

	ns.nomadURL = "http://" + net.JoinHostPort(ns.getComposeServiceIP(c, "nomad"), "4646")
	fmt.Println("nomadURL:", ns.nomadURL)

	var err error
	ns.nomadClient, err = api.NewClient(&api.Config{
		Address: ns.nomadURL,
	})
	c.Check(err, check.IsNil)

	// wait for nomad to elect itself
	err = ns.waitForLeader()
	c.Assert(err, check.IsNil)

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

func (ns *NomadSuite) run(job string, tags []string) error {
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

type input struct {
	NomadAddress string
}

func (ns *NomadSuite) TestExample(c *check.C) {
	fmt.Println("TestExample")
	var err error
	err = ns.run(socat, []string{"treafik.enable=true"})
	c.Assert(err, check.IsNil)

	// and do stuff ..
}

func newJob(tags []string) string {
	for i := 0; i < len(tags); i++ {
		tags[i] = fmt.Sprintf("%q", tags[i])
	}
	return strings.Replace(socat, "TAGS", strings.Join(tags, ", "), 1)
}

const socat = `
job "socat" {
  datacenters = ["dc1"]
  type        = "service"

  group "demo" {
    network {
      mode = "host"
      port "listen" {
        static = 1234
      }
    }

    service {
      provider = "nomad"
      tags = [
        TAGS
      ]
    }

    task "socat" {
      driver = "raw_exec"

      config {
        command = "bash"
        args    = ["-c", "/usr/bin/socat -v tcp-l:1234,fork exec:'/bin/cat'"]
      }

      resources {
        cpu    = 10
        memory = 10
      }
    }
  }
}`
