package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
)

const ConfigFile string = ".outcomes-import.conf"

type config struct {
	Apikey      string `json:"apikey"`
	MigrationId int    `json:"migration_id"`
	Domain      string `json:"domain"`
}

type request struct {
	Body     string
	Apikey   string
	Domain   string
	Method   string
	Endpoint string
}

type importableGuid struct {
	Title string `json:"title"`
	Guid  string `json:"guid"`
}

type migrationIssue struct {
	Id             int    `json:"id"`
	IssueType      string `json:"issue_type"`
	Description    string `json:"description"`
	ErrorReportUrl string `json:"error_report_html_url"`
	ErrorMessage   string `json:"error_message"`
}

type migrationStatus struct {
	Id                   int              `json:"id"`
	WorkflowState        string           `json:"workflow_state"`
	MigrationIssuesCount int              `json:"migration_issues_count"`
	MigrationIssues      []migrationIssue `json:"migration_issues"`
}

type newImport struct {
	MigrationId int    `json:"migration_id"`
	Guid        string `json:"guid"`
}

func configFromFile() *config {
	if f, err := os.Open(configFile()); err == nil {
		var cf config
		if err := json.NewDecoder(f).Decode(&cf); err != nil {
			log.Fatalln("Config file json error:", err)
		}
		return &cf
	} else {
		return nil
	}
}

func (c *config) writeToFile() {
	current := configFromFile()
	// we only want to store the API key if the user already stores it
	if current.Apikey == "" {
		c.Apikey = ""
	}
	b, err := json.MarshalIndent(*c, "", "  ")
	if err != nil {
		log.Fatalln("Error writing to", configFile())
	}
	ioutil.WriteFile(configFile(), b, 0700)
}

func configFile() string {
	return fmt.Sprintf("%s/%s", os.Getenv("HOME"), ConfigFile)
}

func main() {
	var apikey = flag.String("apikey", "", "Canvas API key")
	var domain = flag.String(
		"domain",
		"",
		"The domain.  You can just say the school name if they have a vanity domain, like 'utah' for 'utah.instructure.com' or 'localhost'",
	)
	var status = flag.Int("status", 0, "migration ID to check status")
	var available = flag.Bool("available", false, "Check available migration IDs")
	var guid = flag.String("guid", "", "GUID to schedule for import")
	flag.Parse()

	if cf := configFromFile(); cf != nil {
		if *apikey == "" {
			log.Println("Using API key from config file")
			apikey = &cf.Apikey
		}
		if *status == 0 {
			log.Println("Using migration ID from config file")
			status = &cf.MigrationId
		}
		if *domain == "" {
			log.Println("Using domain from config file")
			domain = &cf.Domain
		}
	}

	req := request{Apikey: *apikey, Domain: *domain}
	verifyRequest(&req)
	req.Domain = normalizeDomain(req.Domain)

	if *available {
		printAvailable(req)
	} else if *guid != "" {
		importGuid(req, *guid)
	} else if *status != 0 {
		getStatus(req, *status)
	} else {
		log.Fatalln("No recent migration ID, and none specified to query status on")
	}
}

func normalizeDomain(domain string) string {
	retval := domain
	if domain == "localhost" {
		return "http://localhost:3000"
		// if we start with http then don't add it, otherwise do
	} else if !strings.HasPrefix(retval, "http") {
		retval = fmt.Sprintf("https://%s", retval)
		if !strings.HasSuffix(retval, "com") && !strings.HasSuffix(retval, "/") {
			retval = fmt.Sprintf("%s.instructure.com", retval)
		}
	}
	return strings.TrimSuffix(retval, "/")
}

func errAndExit(message string) {
	flag.Usage()
	log.Fatalln(message)
	os.Exit(1)
}

func verifyRequest(req *request) {
	if req.Apikey == "" {
		errAndExit("You need a valid canvas API key")
	}
	if req.Domain == "" {
		errAndExit("You must supply a canvas domain")
	}
}

func httpRequest(req request) (*http.Client, *http.Request) {
	client := &http.Client{}
	hreq, err := http.NewRequest(
		req.Method,
		fmt.Sprintf("%s%s", req.Domain, req.Endpoint),
		strings.NewReader(req.Body),
	)
	if err != nil {
		log.Fatalln(err)
	}
	hreq.Header.Add("Authorization", fmt.Sprintf("Bearer %s", req.Apikey))
	return client, hreq
}

func printAvailable(req request) {
	guids := getAvailable(req)
	printImportableGuids(guids)
	(&config{
		Apikey:      req.Apikey,
		Domain:      req.Domain,
		MigrationId: configFromFile().MigrationId,
	}).writeToFile()
}

func getAvailable(req request) []importableGuid {
	req.Body = ""
	req.Method = "GET"
	req.Endpoint = "/api/v1/global/outcomes_import/available"

	client, hreq := httpRequest(req)
	log.Printf("Requesting available guids from %s", hreq.URL)
	resp, err := client.Do(hreq)
	if err != nil {
		log.Fatalln(err)
	}
	defer resp.Body.Close()
	var guids []importableGuid
	if e := json.NewDecoder(resp.Body).Decode(&guids); e != nil {
		log.Fatalln(e)
	}
	return guids
}

func getStatus(req request, migrationId int) {
	req.Body = ""
	req.Method = "GET"
	req.Endpoint = fmt.Sprintf(
		"/api/v1/global/outcomes_import/migration_status/%d",
		migrationId,
	)

	client, hreq := httpRequest(req)

	log.Printf("Retrieving status for migration %d", migrationId)
	resp, err := client.Do(hreq)
	if err != nil {
		log.Fatalln(err)
	}
	defer resp.Body.Close()

	var mstatus migrationStatus
	if e := json.NewDecoder(resp.Body).Decode(&mstatus); e != nil {
		log.Fatalln(e)
	}
	printMigrationStatus(mstatus)
	(&config{
		Apikey:      req.Apikey,
		Domain:      req.Domain,
		MigrationId: migrationId,
	}).writeToFile()
}

func importGuid(req request, guid string) {
	// first check to see if we've been given a title
	guids := getAvailable(req)
	for _, val := range guids {
		if val.Title == guid {
			guid = val.Guid
			break
		}
	}

	req.Body = fmt.Sprintf("guid=%s", guid)
	req.Method = "POST"
	req.Endpoint = "/api/v1/global/outcomes_import/"

	client, hreq := httpRequest(req)

	log.Printf("Requesting import of GUID %s", guid)
	resp, err := client.Do(hreq)
	if err != nil {
		log.Fatalln(err)
	}
	defer resp.Body.Close()

	var nimport newImport
	if e := json.NewDecoder(resp.Body).Decode(&nimport); e != nil {
		log.Fatalln(e)
	}
	printImportResults(nimport)
	(&config{
		Apikey:      req.Apikey,
		Domain:      req.Domain,
		MigrationId: nimport.MigrationId,
	}).writeToFile()
}

func printImportableGuids(guids []importableGuid) {
	fmt.Printf("GUIDs available to import:\n\n")
	for _, guid := range guids {
		fmt.Printf("%s - %s\n", guid.Guid, guid.Title)
	}
}

func printMigrationStatus(mstatus migrationStatus) {
	if mstatus.Id == 0 {
		fmt.Println("\nThe server returned an error.  Are you sure that migration ID exists?")
	} else {
		fmt.Printf("\nMigration status for migration '%d':\n", mstatus.Id)
		fmt.Printf(" - Workflow state: %s\n", mstatus.WorkflowState)
		fmt.Printf(" - Migration issues count: %d\n", mstatus.MigrationIssuesCount)
		fmt.Printf(" - Migration issues:\n")
		for _, val := range mstatus.MigrationIssues {
			fmt.Printf("   - ID: %d\n", val.Id)
			fmt.Printf("   - Link: %s\n", val.ErrorReportUrl)
			fmt.Printf("   - Issue type: %s\n", val.IssueType)
			fmt.Printf("   - Error message: %s\n", val.ErrorMessage)
			fmt.Printf("   - Description: %s\n", val.Description)
		}
	}
}

func printImportResults(nimport newImport) {
	fmt.Printf(
		"\nMigration ID is %d\n",
		nimport.MigrationId,
	)
}
