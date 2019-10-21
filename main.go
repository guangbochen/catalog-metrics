package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"time"

	_ "github.com/influxdata/influxdb1-client" // this is important because of the bug in go mod
	influxClient "github.com/influxdata/influxdb1-client"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

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

const (
	baseRepoURL  = "https://hub.docker.com/v2/repositories/"
	repoName = "ranchercharts"
	VERSION = "0.1.0-dev"
	httpRetries = 3

)

var tr = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
var hc = &http.Client{ Timeout: 10 * time.Second, Transport: tr}

func main(){
	app := cli.NewApp()
	app.Name = "catalog-metrics"
	app.Usage = "helps to collecting rancher catalog metrics"
	app.Version = VERSION
	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name: "debug",
		},
	}
	app.Commands = []cli.Command{
		{
			Name:    "dockerhub",
			Aliases: []string{"hub"},
			Usage:   "run to collecting repository metrics of ranchercharts",
			Action:  run,
		},
	}
	sort.Sort(cli.FlagsByName(app.Flags))
	sort.Sort(cli.CommandsByName(app.Commands))

	if err := app.Run(os.Args); err != nil {
		logrus.Fatal(err)
	}

}

func run(c *cli.Context) error{
	if c.Bool("debug") {
		logrus.SetLevel(logrus.DebugLevel)
	}
	server := os.Getenv("INFLUX_SERVER")
	port := os.Getenv("INFLUX_PORT")
	if len(server) == 0 ||  len(port) == 0 {
		return fmt.Errorf("influxdb server and port number is required, %s, %s", server, port)
	}
	portNum, err := strconv.ParseInt(port, 10, 32)
	if err != nil {
		return err
	}
	conn, err := newInfluxDBClient(server, int(portNum))
	if err != nil {
		return err
	}

	// get ranchercharts repository metadata from dockerHub
	repo, err := getRepositoryJson(baseRepoURL + repoName)
	if err != nil {
		return err
	}
	if err = writeDataIntoInfluxDB(conn, *repo); err != nil {
		return err
	}

	// get all repository metadata by pagination link
	for {
		repo, err = getRepositoryJson(repo.Next)
		if err != nil {
			return err
		}

		if err = writeDataIntoInfluxDB(conn, *repo); err != nil {
			return err
		}

		if repo.Next == "" {
			break
		}
	}
	return nil
}

func getRepositoryJson(url string) (*Repository, error) {
	logrus.Infof("get repository metadata with url: %s", url)
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
			logrus.WithFields(logrus.Fields{
				"attempt": retries + 1,
				"error":   err,
				"data":    url,
			}).Warn("failed to retrieve metadata")
			continue
		}
		err = json.NewDecoder(resp.Body).Decode(&repo)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"attempt": retries + 1,
				"error":   err,
				"data":    resp.Body,
			}).Warn("failed to decode metadata")
			continue
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
		UnsafeSsl: true,
	}
	client, err := influxClient.NewClient(conf)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func writeDataIntoInfluxDB(client *influxClient.Client, repo Repository) error{
	size := len(repo.Results)
	pts  := make([]influxClient.Point, size)

	dbConfig := getInfluxDBConfig()
	logrus.Debugf("write data to the influxDB: %+v", repo.Results)
	for i := 0; i < size; i++ {
		repo := repo.Results[i]
		pts[i] = influxClient.Point{
			Measurement: dbConfig.Measure,
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
			Precision: "h",
		}
	}
	bps := influxClient.BatchPoints{
		Points:          pts,
		Database:        dbConfig.DB,
		RetentionPolicy: "autogen",
	}
	_, err := client.Write(bps)
	if err != nil {
		return err
	}
	logrus.Infof("success write %d data into the influxDB", len(repo.Results))
	return nil
}

type InfluxDBConf struct {
	DB string
	Measure string
}

func getInfluxDBConfig() *InfluxDBConf {
	var influx = &InfluxDBConf{}
	measure := os.Getenv("INFLUXDB_MEASURE")
	db := os.Getenv("INFLUXDB_NAME")

	if len(measure) == 0 {
		influx.Measure = "repositories"
	}

	if len(db) == 0 {
		influx.DB = "catalog"
	}
	return influx
}
