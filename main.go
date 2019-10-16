package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	_ "github.com/influxdata/influxdb1-client" // this is important because of the bug in go mod
	influxClient "github.com/influxdata/influxdb1-client"
)

const httpRetries = 3
var hc = &http.Client{Timeout: 10 * time.Second}

type Repository struct {
	Count int `json:"count"`
	Next string `json:"next"`
	Previous string `json:"previous"`
	Results []Repo `json:"results"`
}

type Repo struct {
	User string `json:"user"`
	Name string `json:"name"`
	Namespace string `json:"namespace"`
	RepositoryType string `json:"repository_type"`
	Status int `json:"status"`
	Description string `json:"description"`
	IsPrivate bool `json:"is_private"`
	IsAutomated bool `json:"is_automated"`
	CanEdit bool `json:"can_edit"`
	StarCount int `json:"star_count"`
	PullCount int `json:"pull_count"`
	LastUpdated string `json:"last_updated"`
	IsMigrated bool `json:"is_migrated"`
}

const baseRepoURL  = "https://hub.docker.com/v2/repositories/"
const repoName = "ranchercharts"

func main(){
	server := os.Getenv("INFLUX_SERVER")
	port := os.Getenv("INFLUX_PORT")
	if len(server) == 0 ||  len(port) == 0 {
		log.Fatalf("both influxdb server and port is required, %s, %s", server, port)
	}
	portNum, err := strconv.ParseInt(port, 10, 32)
	if err != nil {
		log.Fatalf("%v", err.Error())
	}
	conn, err := newInfluxDBClient(server, int(portNum))
	if err != nil {
		log.Fatalf("%v", err.Error())
	}

	// get rancher/charts repository metadata from dockerHub and write into influxDB
	repo, err := getRepositoryJson(baseRepoURL + repoName)
	if err != nil {
		fmt.Errorf("%v", err.Error())
	}
	err = writeDataIntoInfluxDB(conn, *repo, "")
	if err != nil {
		fmt.Errorf("%v", err.Error())
	}

	// loop to get all of the repository by pagination link
	for {
		repo, err = getRepositoryJson(repo.Next)
		if err != nil {
			fmt.Errorf("%v", err.Error())
		}
		fmt.Printf("%+v\n", repo)

		err = writeDataIntoInfluxDB(conn, *repo, "")
		if err != nil {
			fmt.Errorf("%v", err.Error())
		}

		if repo.Next == "" {
			break
		}
	}
}

func getRepositoryJson(url string) (*Repository, error) {
	fmt.Println(url)
	failureInterval := 10 * time.Second
	var repo = Repository{}
	var err error

	// Doing max 3 retries to get repository metadata
	for retries := 0; retries <= httpRetries; retries++ {
		if retries > 0 {
			time.Sleep(failureInterval)
		}
		resp, err := hc.Get(url)
		if err != nil {
			return nil, err
		}
		err = json.NewDecoder(resp.Body).Decode(&repo)
		if err != nil {
			fmt.Printf("failed to get repository with err: %s, retry %d times\n", err.Error(), retries+1)
			return nil, err
		}
		resp.Body.Close()
		if err == nil {
			break
		}
	}
	return &repo, err
}

func newInfluxDBClient(endpoint string, port int) (*influxClient.Client, error) {
	host, err := url.Parse(fmt.Sprintf("http://%s:%d", endpoint, port))
	if err != nil {
		return nil, err
	}

	// NOTE: this assumes you've setup a user and have setup shell env variables,
	// namely INFLUX_USER/INFLUX_PWD. If not just omit Username/Password below.
	conf := influxClient.Config{
		URL:      *host,
		Username: os.Getenv("INFLUX_USER"),
		Password: os.Getenv("INFLUX_PWD"),
	}
	client, err := influxClient.NewClient(conf)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func writeDataIntoInfluxDB(client *influxClient.Client, repo Repository, measure string) error{
	if measure == "" {
		measure = "repositories4"
	}
	size := len(repo.Results)
	pts  := make([]influxClient.Point, size)

	for i := 0; i < size; i++ {
		repo := repo.Results[i]
		pts[i] = influxClient.Point{
			Measurement: "repositories3",
			Tags: map[string]string{
				"name": repo.Name,
				"namespace": repo.Namespace,
			},
			Fields: map[string]interface{}{
				"user": repo.User,
				"repository_type": repo.RepositoryType,
				"status": repo.Status,
				"description": repo.Description,
				"is_private": repo.IsPrivate,
				"is_automated": repo.IsAutomated,
				"can_edit": repo.CanEdit,
				"star_count": repo.StarCount,
				"pull_count": repo.PullCount,
				"last_updated": repo.LastUpdated,
				"is_migrated": repo.IsMigrated,
			},
			Time:      time.Now(),
			Precision: "ms",
		}
	}
	bps := influxClient.BatchPoints{
		Points:          pts,
		Database:        "catalogtest",
		RetentionPolicy: "autogen",
	}
	_, err := client.Write(bps)
	return err
}
