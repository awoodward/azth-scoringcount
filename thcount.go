package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/tarm/serial"
)

// constants that effect the operation of the program
const carMax = 100
const clueNum = 26
const scannerMax = 20
const carStateFile = "carstate.json"
const timeStateFile = "timestate.json"

const emergencyOffset = 0 // emergencies are first in the matrix
const clueOffset = emergencyOffset + clueNum
const totalCol = (clueNum * 2) + 1 + emergencyOffset

var quit bool
var thCommand *string

type carTime struct {
	checkOut time.Time
	checkIn  time.Time
}

type CarData struct {
	CarNum        int
	Scanned       bool
	Emergencies   int
	Clues         int
	EmergencyList string
	ClueList      string
	ScanTime      time.Time
}

type TallyData struct {
	TotalClues         int
	CountedClues       int
	TotalEmergencies   int
	CountedEmergencies int
}

type ScannerData struct {
	ScannerNum   int
	ScanCount    int
	LastScanTime time.Time
}

type CarPageData struct {
	Title    string
	Cars     []CarData
	Tally    TallyData
	Scanners []ScannerData
}

//var thTimes *[carMax]carTime

type countData struct {
	debug    bool
	thCount  *[carMax][totalCol]bool
	scanTime *[carMax]time.Time
	thTimes  *[carMax]carTime
	scanners *[scannerMax]ScannerData
}

func (c *countData) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	c.status()
	query := req.URL.Path

	log.Printf("Request Path : %v from: %v\n", query, req.RemoteAddr)
	if strings.HasPrefix(query, "/download") {
		c.saveData()
		timestr := time.Now().Format("2006-01-02_03-04")
		filename := fmt.Sprintf("%v.txt", timestr)
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Disposition", "attachment; filename="+filename)
		c.writeTextStream(w)
		return
	}

	if strings.HasPrefix(query, "/save") {
		c.saveData()
		return
	}

	var carData CarPageData
	carData.Cars = c.buildCarData()
	carData.Title = "Cars!"
	carData.Tally = c.getTally()
	carData.Scanners = make([]ScannerData, 0)
	for i := 0; i < scannerMax; i++ {
		if c.scanners[i].ScanCount > 0 {
			carData.Scanners = append(carData.Scanners, c.scanners[i])
		}
	}
	sortOrder := req.URL.Query().Get("sort")
	switch sortOrder {
	case "leader":
		// Sort data by clues and emergencies
		sort.SliceStable(carData.Cars, func(i, j int) bool {
			if i == 0 {
				// 0 index always first
				//return false
			}
			if (carData.Cars[i].Scanned != carData.Cars[j].Scanned) && !carData.Cars[j].Scanned {
				return true
			}
			if carData.Cars[j].Clues != carData.Cars[i].Clues {
				return carData.Cars[j].Clues < carData.Cars[i].Clues
			}
			return carData.Cars[i].Emergencies < carData.Cars[j].Emergencies
		})
	}
	//log.Printf("Sort: %v\n", sortOrder)
	tmpl := template.Must(template.ParseFiles("templates/template.html"))
	tmpl.Execute(w, carData)
}

const (
	usage = `usage: %s

Program for barcode counting clue sheets and emergencies at check-in
for Arizona Treasure Hunt
Written by: Andy Woodward - TH Committee 2019-2022
Email: awoodward@gmail.com

Options:
`
)

func (c *countData) getCarEmergencies(car int) string {
	// process emergencies
	emergencies := ""
	for i := 1 + emergencyOffset; i <= clueNum; i++ {
		if c.thCount[car][i] == false {
			// emergency wasn't found so must've been opened
			if len(emergencies) > 0 {
				emergencies = emergencies + ", "
			}
			emergencies = emergencies + strconv.Itoa(i)
		}
	}
	return emergencies
}

func (c *countData) getCarClues(car int) string {
	// This code is hideous but it works. Don't judge me.
	type streak struct {
		start int
		end   int
	}

	var streaks []streak

	var currentStreak streak
	//currentStreak := new(streak)
	end := clueOffset
	start := 0
	clue := 0
	// first handle rollover
	if (c.thCount[car][clueOffset+clueNum] == false) && (c.thCount[car][clueOffset+1] == false) {
		// Z and A are populated
		// find the start of the streak
		first := 0
		for i := clueNum; i >= 1; i-- {
			if c.thCount[car][clueOffset+i] == false {
				first = i
				clue++
			} else {
				// start of streak found
				currentStreak.start = first
				end = first
				break
			}
		}
		if clue == clueNum {
			// all clues found
			currentStreak = streak{1, clueNum}
		} else {
			last := 0
			for i := 1; i < end; i++ {
				if c.thCount[car][clueOffset+i] == false {
					last = i
				} else {
					// start of streak found
					currentStreak.end = last
					start = last
					break
				}
			}
		}
		streaks = append(streaks, currentStreak)
		end--
	}

	if clue != clueNum {
		currentStreak = streak{0, 0}
		for i := start + 1; i <= end; i++ {
			if c.thCount[car][clueOffset+i] == false {
				// got a clue
				if currentStreak.start == 0 {
					currentStreak.start = i
					currentStreak.end = i
				} else {
					currentStreak.end = i
				}
			} else {
				if currentStreak.start != 0 {
					streaks = append(streaks, currentStreak)
					currentStreak = streak{0, 0}
				}
			}
		}
		if currentStreak.start != 0 {
			// append the last streak
			streaks = append(streaks, currentStreak)
		}
	}
	streakStr := ""
	if len(streaks) > 0 {
		for i := 0; i < len(streaks); i++ {
			if len(streakStr) > 0 {
				streakStr = streakStr + ", "
			}
			streakStr = streakStr + string(rune(64+streaks[i].start))
			if streaks[i].start != streaks[i].end {
				streakStr = streakStr + "-" + string(rune(64+streaks[i].end))
			}
		}
	}
	return strings.ToLower(streakStr)
}

func (c *countData) hasCars() bool {
	cars := 0
	for i := 1; i < carMax; i++ {
		if c.thCount[i][0] == false {
			// no scans for car
			continue
		}
		cars++
	}
	if cars > 0 {
		return true
	}
	return false
}

func (c *countData) status() {
	cars := 0
	for i := 1; i < carMax; i++ {
		if c.thCount[i][0] == false {
			// no scans for car
			continue
		}
		cars++
	}
	log.Printf("%v cars counted\n", cars)
}

func (c *countData) buildCarData() []CarData {
	carList := make([]CarData, carMax)
	for i := 1; i < carMax; i++ {
		var currentCar CarData
		currentCar.CarNum = i
		currentCar.Scanned = c.thCount[i][0]
		emergencyStr := c.getCarEmergencies(i)
		clueStr := c.getCarClues(i)
		currentCar.EmergencyList = emergencyStr
		currentCar.ClueList = clueStr
		currentCar.Emergencies, currentCar.Clues = c.getSolveCount(i)
		currentCar.Emergencies = clueNum - currentCar.Emergencies
		currentCar.Clues = clueNum - currentCar.Clues
		currentCar.ScanTime = c.scanTime[i]
		carList[i] = currentCar
	}
	return carList
}

func (c *countData) writeTextStream(f io.Writer) error {
	carList := c.buildCarData()
	for i := 1; i < carMax; i++ {
		line := fmt.Sprintf("\t%v\t0\t%v\t%v\n", i, carList[i].ClueList, carList[i].EmergencyList)
		_, err := io.WriteString(f, line)
		if err != nil {
			fmt.Println(err)
			//f.Close()
			return err
		}
	}
	return nil
}

func (c *countData) saveData() {
	writeState(c)
	if !c.hasCars() {
		// nothing to save
		log.Println("No data to save")
		return
	}
	timestr := time.Now().Format("2006-01-02_03-04")
	filename := fmt.Sprintf("%v.txt", timestr)
	f, err := os.Create(filename)
	if err != nil {
		fmt.Println(err)
		return
	}
	// Send the data to file
	err = c.writeTextStream(f)
	if err != nil {
		log.Printf("Error saving data file: %v\n", err)
	}

	f.Close()
	log.Printf("Data saved to file: %v\n", filename)
}

func (c *countData) getSolveCount(car int) (int, int) {
	emergencies := 0
	clues := 0
	for i := 1; i < 53; i++ {
		if c.thCount[car][i] == true {
			if i <= clueNum {
				emergencies++
			} else {
				clues++
			}
		}
	}
	return emergencies, clues
}

func (c *countData) processCode(code string) bool {
	if len(code) == 0 {
		// Windows seems to return a zero length string
		return true
	}
	features := strings.Split(code, "-")
	if len(features) != 3 {
		//log.Printf("Invalid number of code segments (%v) %v (%v)\n", len(features), code, len(code))
		return false
	}
	car, _ := strconv.Atoi(features[0])
	cmd := features[1]
	// bounds checking
	if car >= carMax {
		log.Printf("Invalid car number %v. Max cars is %v. Command: %v\n", car, carMax, cmd)
		return false
	}
	c.thCount[car][0] = true
	switch cmd {
	case "QUIT":
		log.Println("Quitting...")
		c.saveData()
		quit = true
	case "CLEAR":
		if car == 0 {
			c.saveData()
			c.thCount = new([carMax][totalCol]bool)
			c.thTimes = new([carMax]carTime)
			log.Println("All data cleared")
		} else {
			// clear car
			for i := 0; i < totalCol; i++ {
				c.thCount[car][i] = false
			}
			log.Printf("Car %v data cleared", car)
		}
	case "SAVE":
		c.saveData()
	case "STATUS":
		c.status()
	case "CL": // Clue
		clue := features[2][0] // get the first character
		clue = clue - 64
		c.thCount[car][clueOffset+clue] = true
		c.scanTime[car] = time.Now()
	case "EM": // Emergency
		emergency, _ := strconv.Atoi(features[2])
		c.thCount[car][emergency] = true
		c.scanTime[car] = time.Now()
	case "CA": // Car
		switch *thCommand {
		case "count":
			emergencies, clues := c.getSolveCount(car)
			/*
				emergencies := 0
				clues := 0
				for i := 1; i < 53; i++ {
					if count.thCount[car][i] == true {
						if i <= clueNum {
							emergencies++
						} else {
							clues++
						}
					}
				}
			*/
			//log.Printf("car: %v emergencies: %v clues: %v\n", car, emergencies, clues)
			//log.Printf("car: %v emergency result %v clue result %v\n", car, getCarEmergencies(car), getCarClues(car))
			fmt.Println("--------------------")
			fmt.Printf("Car: %v scans: emergencies: %v \t clues: %v\n", car, emergencies, clues)
			fmt.Printf("Car: %v emergencies opened (%v): %v \n", car, clueNum-emergencies, c.getCarEmergencies(car))
			fmt.Printf("Car: %v clues visited (%v): %v\n", car, clueNum-clues, c.getCarClues(car))
		case "checkin":
			c.thTimes[car].checkIn = time.Now()
			log.Printf("Car %v check-in time: %v\n", car, c.thTimes[car].checkIn.Format("15:04:05"))
		case "checkout":
			c.thTimes[car].checkOut = time.Now()
			log.Printf("Car %v check-out time: %v\n", car, c.thTimes[car].checkOut.Format("15:04:05"))
		}
	}
	return true
}

func (c *countData) getTally() TallyData {
	var tally TallyData
	tally.TotalClues = carMax * clueNum
	tally.TotalEmergencies = tally.TotalClues
	for i := 0; i < carMax; i++ {
		for j := 0; j < clueNum; j++ {
			if c.thCount[i][j] == true {
				tally.CountedEmergencies++
			}
		}
		for j := clueOffset; j < totalCol; j++ {
			if c.thCount[i][j] == true {
				tally.CountedClues++
			}
		}
	}
	return tally
}

func worker(s *serial.Port, codes chan string, workerId int, count *countData) {
	errorCount := 0
	buf := make([]byte, 256)
	lastVal := ""
	count.scanners[workerId].ScannerNum = workerId
	for {
		if quit {
			return
		}
		//log.Println("Ready")
		if errorCount > 10 {
			log.Println("Too many errors")
			quit = true
			//close(codes)
			return
		}
		n, err := s.Read(buf)
		if err != nil {
			if strings.Compare(err.Error(), "EOF") == 0 {
				// Normal end-of-file - nothing to read
				//log.Println("EOF")
			} else {
				log.Printf("Error: %v\n", err)
				errorCount++
			}
			continue
		}
		errorCount = 0
		code := string(buf[:n])
		//code = strings.TrimSuffix(code, "\r")
		//code = strings.TrimSuffix(code, "\n")
		//code = strings.TrimSpace(code)
		if len(code) == 0 {
			continue
		}
		count.scanners[workerId].LastScanTime = time.Now()
		code = lastVal + code
		if count.debug {
			log.Printf("[%v]length: %v data: %q code: %v codeLen: %v\n", workerId, n, buf[:n], code, len(code))
		}
		codes := strings.Split(code, "\n")
		for i, v := range codes {
			v = strings.TrimSpace(v)
			valid := count.processCode(v)
			if valid {
				count.scanners[workerId].ScanCount++
			}
			if count.debug {
				log.Printf("Code: %v, Len: %v, Current: %v, Valid: %v\n", v, len(codes), i, valid)
			}
			if i == len(codes)-1 {
				if valid {
					lastVal = ""
				} else {
					lastVal = v
				}
			}
		}
		//data = append(data, buf[:n]...)
		//codes <- code
	}

}

func init() {
	thCommand = flag.String("command", "count", "Current command")
}

func (c *countData) readState(carFilename string, timeFilename string) {
	_, err := os.Stat(carFilename)
	if os.IsNotExist(err) {
		// doesn't exist; initialize it
		//componentClasses = make([]componentClass, 0)
	} else {
		// read metadata from file
		byteValue, _ := ioutil.ReadFile(carFilename)

		// Unmarshal our byteArray which contains the
		// jsonFile's content into 'users' which we defined above
		json.Unmarshal(byteValue, &c.thCount)
	}

	_, err = os.Stat(timeFilename)
	if os.IsNotExist(err) {
		// doesn't exist; initialize it
		//componentClasses = make([]componentClass, 0)
	} else {
		// read metadata from file
		byteValue, _ := ioutil.ReadFile(timeFilename)

		// we unmarshal our byteArray which contains our
		// jsonFile's content into 'users' which we defined above
		json.Unmarshal(byteValue, &c.thTimes)
	}

}

func writeState(count *countData) {
	file, _ := json.MarshalIndent(count.thCount, "", " ")
	ioutil.WriteFile(carStateFile, file, 0644)

	file, _ = json.MarshalIndent(count.thTimes, "", " ")
	ioutil.WriteFile(timeStateFile, file, 0644)
}

// *** Windows ***/
const WINDOWS_SERIAL = "wmic path Win32_PnPEntity Get Name"

func getWindowsDevices() []string {
	//get the list from wmic
	getPorts := exec.Command("cmd", "/C", WINDOWS_SERIAL)
	raw, err := getPorts.CombinedOutput()
	if err != nil {
		return nil
	}
	list := strings.Split(string(raw), "\r\n")
	ports := make([]string, 0, 10)
	regex, _ := regexp.Compile("(COM[0-9]+)")
	for _, v := range list {
		matches := regex.FindAllString(v, 1)
		if len(matches) == 1 {
			ports = append(ports, matches[0])
		}
	}
	return testPorts(ports)
}

func testPorts(p []string) []string {
	d := make([]string, 0, len(p))
	//buf := make([]byte, 128)
	for _, port := range p {
		//try to open the port and read back its status string
		c := &serial.Config{Name: port, Baud: 115200, ReadTimeout: time.Second * 5}
		s, err := serial.OpenPort(c)
		if err != nil {
			fmt.Println("Error: ", err)
			continue
		}
		defer s.Close()
		d = append(d, port)
		/*
			time.Sleep(time.Millisecond * 2500)
			n, _ := s.Read(buf)
			if n > 0 {
				dat := string(buf[:n])
				log.Println("Device " + port + " returned: " + dat)
				matcher, _ := regexp.Compile("(SYS|838983)\t(VER|866982)")
				if match := matcher.Find(buf[:n]); match != nil {
					d = append(d, Port{PortId: port, Status: dat})
				}
				continue
			}
		*/
	}
	return d
}

func GetOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP.String()
}

/*
func getRoot(w http.ResponseWriter, req *http.Request) {
	var carData CarPageData
	carData.Cars = buildCarData()
	carData.Title = "Cars!"
	sortOrder := req.URL.Query().Get("sort")
	switch sortOrder {
	case "leader":
		// Sort data by clues and emergencies
		sort.SliceStable(carData.Cars, func(i, j int) bool {
			if i == 0 {
				// 0 index always first
				//return false
			}
			if (carData.Cars[i].Scanned != carData.Cars[j].Scanned) && !carData.Cars[j].Scanned {
				return true
			}
			if carData.Cars[j].Clues < carData.Cars[i].Clues {
				return true
			}
			return carData.Cars[i].Emergencies < carData.Cars[j].Emergencies
		})
	}
	//log.Printf("Sort: %v\n", sortOrder)
	tmpl := template.Must(template.ParseFiles("templates/template.html"))
	tmpl.Execute(w, carData)
}

func getDownload(w http.ResponseWriter, req *http.Request) {
	timestr := time.Now().Format("2006-01-02_03-04")
	filename := fmt.Sprintf("%v.txt", timestr)
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	writeTextStream(w)
}
*/

func main() {
	/*
		count.thCount = new([carMax][53]bool)
		//count.thCount[1][clueOffset+1] = true
		//count.thCount[1][clueOffset+5] = true
		//count.thCount[1][clueOffset+6] = true
		//count.thCount[1][clueOffset+7] = true
		count.thCount[1][clueOffset+13] = true
		count.thCount[1][clueOffset+15] = true
		//count.thCount[1][clueOffset+20] = true

		result := getCarClues(1)
		log.Println(result)
		return
	*/

	//thCount = new([carMax][totalCol]bool)
	//thTimes = new([carMax]carTime)
	//scanTime = new([carMax]time.Time)
	var count countData
	count.thCount = new([carMax][totalCol]bool)
	count.thTimes = new([carMax]carTime)
	count.scanTime = new([carMax]time.Time)
	count.scanners = new([scannerMax]ScannerData)

	flag.Parse()
	fmt.Println("Command: ", *thCommand)

	f, err := os.OpenFile("thcount.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	defer f.Close()

	//errorCount := 0

	mw := io.MultiWriter(os.Stdout, f)
	log.SetOutput(mw)

	count.readState(carStateFile, timeStateFile)

	osType := runtime.GOOS
	var patterns []string
	switch osType {
	case "darwin": // Mac OS X
		patterns = []string{"/dev/cu.usbmodem*", "/dev/cu.usbserial*"}
	case "linux":
		patterns = []string{"/dev/serial/by-id/*"}
	case "windows":
		patterns = []string{"COM[0-9]+"}
	default:
		log.Fatal("OS Not Supported ", osType)

	}

	var portName string

	var portNames []string
	for _, pattern := range patterns {
		if osType == "windows" {
			portNames = getWindowsDevices()
			if len(portNames) > 0 {
				portName = portNames[0]
			}
		} else {
			matches, err := filepath.Glob(pattern)
			if err != nil {
				log.Printf("Error: %v\n", err)
			}
			if len(matches) != 0 {
				for i := 0; i < len(matches); i++ {
					portNames = append(portNames, matches[i])
				}
				portName = matches[0]
				log.Printf("Found : %v\n", matches)
			}
		}
	}

	if len(portName) == 0 {
		log.Fatal("No serial barcode scanner device found")
	}

	var wg sync.WaitGroup
	for i, v := range portNames {
		// Create a worker for each serial device detected
		log.Printf("Using serial device: [%v]%s\n", i, v)
		c := &serial.Config{Name: v, Baud: 19200, ReadTimeout: time.Second * 1}
		s, err := serial.OpenPort(c)
		if err != nil {
			log.Fatal(err)
		}

		//codes := make(chan string, 20)
		wg.Add(1)
		go func(i int, count *countData) {
			defer wg.Done()
			worker(s, nil, i, count)
		}(i, &count)
	}
	// start HTTP as a function
	myIp := GetOutboundIP()
	fmt.Printf("listen on http://%v:8080\n", myIp)
	mux := http.NewServeMux()
	//mux.HandleFunc("/", getRoot)
	mux.Handle("/", &count)
	//mux.HandleFunc("/download", getDownload)
	wg.Add(1)
	go func(mux *http.ServeMux) {
		defer wg.Done()
		log.Fatal(http.ListenAndServe(":8080", mux))
	}(mux)

	wg.Wait()
}
