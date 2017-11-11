package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/shurcooL/githubql"
	"github.com/urfave/cli"
	"golang.org/x/oauth2"
)

// CLI Settings
var (
	appName    = "peekabow"
	appUsage   = "show your repo's summary of ZenHub pipeline"
	appVersion = "0.0.1"
)

// Token Settings
type (
	Config struct {
		Token TokenConfig
	}
	TokenConfig struct {
		GitHub string
		ZenHub string
	}
)

// GitHub Queries
type IssueFragment struct {
	Title string
	Url   string
}

var (
	findRepoId struct {
		Repository struct {
			DatabaseId int
		} `graphql:"repository(owner: $owner, name: $repo)"`
	}
	findIssue struct {
		Repository struct {
			IssueOrPullRequest struct {
				IssueFragment `graphql:"... on Issue"`
			} `graphql:"issueOrPullRequest(number: $number)"`
		} `graphql:"repository(owner: $owner, name: $repo)"`
	}
)

// ZenHub Types
type (
	Pipelines []Pipeline
	Board     struct {
		Pipelines Pipelines
	}
	Pipeline struct {
		Name   string
		Issues []Issue
	}
	Issue struct {
		IssueNumber int `json:"issue_number"`
	}
)

func (self Pipelines) Find(f func(Pipeline) bool) (Pipeline, bool) {
	for _, pipeline := range self {
		if f(pipeline) {
			return pipeline, true
		}
	}
	return Pipeline{}, false
}

var (
	tomlData     string
	github       *githubql.Client
	owner        string
	repo         string
	pipelineName string
)

func main() {
	user, err := user.Current()
	if err != nil {
		panic(err)
	}
	tomlData = user.HomeDir + "/.config/peekabow/config.toml"

	app := cli.NewApp()
	app.Name = appName
	app.Usage = appUsage
	app.Version = appVersion

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "owner, o",
			Value: "",
			Usage: "repository owner",
		},
		cli.StringFlag{
			Name:  "repo, r",
			Value: "",
			Usage: "repository name",
		},
		cli.StringFlag{
			Name:  "pipeline, p",
			Value: "",
			Usage: "pipeline name of ZenHub",
		},
		cli.BoolFlag{
			Name:  "verbose",
			Usage: "show debug log",
		},
	}

	app.Commands = []cli.Command{
		{
			Name:    "issues",
			Aliases: []string{"i"},
			Usage:   "show your repo's pipeline issue summary",
			Action:  showIssues,
		},
	}

	app.Run(os.Args)
}

func log(c *cli.Context, a ...interface{}) {
	if c.GlobalBool("verbose") {
		fmt.Println(a...)
	}
}

func showIssues(c *cli.Context) {
	log(c, "👀  Load global flags...")
	owner = c.GlobalString("owner")
	repo = c.GlobalString("repo")
	pipelineName = c.GlobalString("pipeline")
	if owner == "" || repo == "" || pipelineName == "" {
		fmt.Println("❌  Please set --owner, --repo, and --pipeline. See help.")
		return
	}

	log(c, "📄  Load token settings from toml...")
	var config Config
	if _, err := toml.DecodeFile(tomlData, &config); err != nil {
		panic(err)
	}
	var count = 0
	if config.Token.GitHub == "" {
		count += 1
	}
	if config.Token.ZenHub == "" {
		count += 1
	}
	if count > 0 {
		var example string = "[token]\ngithub = \"xxx...\"\nzenhub = \"yyy...\""
		fmt.Println("❌  Please set your token in " + tomlData + ". Just like below:\n" + example)
		return
	}

	log(c, "🔍  Search repository ID from GitHub...")
	src := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: config.Token.GitHub})
	httpClient := oauth2.NewClient(context.Background(), src)
	github = githubql.NewClient(httpClient)

	args := map[string]interface{}{
		"owner": githubql.String(owner),
		"repo":  githubql.String(repo),
	}
	query := findRepoId
	if err := github.Query(context.Background(), &query, args); err != nil {
		panic(err)
	}
	log(c, "👍  Found repository ID: "+strconv.Itoa(query.Repository.DatabaseId))

	log(c, "🔍  Search pipeline issues from ZenHub...")
	zenHub := &http.Client{}
	req, _ := http.NewRequest(
		"GET",
		"https://api.zenhub.io/p1/repositories/"+strconv.Itoa(query.Repository.DatabaseId)+"/board",
		nil,
	)
	req.Header.Add("X-Authentication-Token", config.Token.ZenHub)
	resp, _ := zenHub.Do(req)
	defer resp.Body.Close()
	body, _ := ioutil.ReadAll(resp.Body)
	board := new(Board)
	if err := json.Unmarshal(body, board); err != nil {
		panic(err)
	}
	p, found := board.Pipelines.Find(func(p Pipeline) bool {
		return p.Name == pipelineName
	})

	// convert issue into formatted message
	if !found {
		fmt.Println("❌  Not Found: " + pipelineName)
		fmt.Println("❌  Please check whether if your ZenHub pipeline's name is correct.")
		return
	} else {
		fmt.Println("🔽  " + owner + "/" + repo + ": " + pipelineName + "'s issues here:")
		c := toNumber(p.Issues)
		out := message(c)
		var buffer bytes.Buffer
		for msg := range out {
			buffer.WriteString(msg + "\n")
		}
		if summary := strings.TrimSuffix(buffer.String(), "\n"); summary != "" {
			fmt.Println(summary)
		} else {
			fmt.Println("😄  No Issue")
		}
	}
}

func toNumber(issues []Issue) <-chan int {
	out := make(chan int)
	go func() {
		for _, issue := range issues {
			out <- issue.IssueNumber
		}
		close(out)
	}()
	return out
}

func message(in <-chan int) <-chan string {
	out := make(chan string)
	go func() {
		for num := range in {
			args := map[string]interface{}{
				"owner":  githubql.String(owner),
				"repo":   githubql.String(repo),
				"number": githubql.Int(num),
			}
			query := findIssue
			err := github.Query(context.Background(), &query, args)
			if err != nil {
				panic(err)
			}
			if issue := query.Repository.IssueOrPullRequest.IssueFragment; issue.Title != "" {
				out <- "#" + strconv.Itoa(num) + ": " + issue.Title + " : " + issue.Url
			}
		}
		close(out)
	}()
	return out
}
