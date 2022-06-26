package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tarm/serial"
)

const carMax = 100
const emergencyOffset = 0
const clueNum = 26
const clueOffset = emergencyOffset + clueNum
const carStateFile = "carstate.json"
const timeStateFile = "timestate.json"
const totalCol = (clueNum * 2) + 1 + emergencyOffset

var thCount *[carMax][totalCol]bool
var quit bool
var thCommand *string

type carTime struct {
	checkOut time.Time
	checkIn  time.Time
}

var thTimes *[carMax]carTime

const (
	usage = `usage: %s

Program for barcode counting clue sheets and emergencies at check-in
for Arizona Treasure Hunt
Written by: Andy Woodward - TH Committee 2019-2022
Email: awoodward@gmail.com

Options:
`
)

func getCarEmergencies(car int) string {
	// process emergencies
	emergencies := ""
	for i := 1 + emergencyOffset; i <= clueNum; i++ {
		if thCount[car][i] == false {
			// emergency wasn't found so must've been opened
			if len(emergencies) > 0 {
				emergencies = emergencies + ", "
			}
			emergencies = emergencies + strconv.Itoa(i)
		}
	}
	return emergencies
}

func getCarClues(car int) string {
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
	count := 0
	// first handle rollover
	if (thCount[car][clueOffset+clueNum] == false) && (thCount[car][clueOffset+1] == false) {
		// Z and A are populated
		// find the start of the streak
		first := 0
		for i := clueNum; i >= 1; i-- {
			if thCount[car][clueOffset+i] == false {
				first = i
				count++
			} else {
				// start of streak found
				currentStreak.start = first
				end = first
				break
			}
		}
		if count == clueNum {
			// all clues found
			currentStreak = streak{1, clueNum}
		} else {
			last := 0
			for i := 1; i < end; i++ {
				if thCount[car][clueOffset+i] == false {
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

	if count != clueNum {
		currentStreak = streak{0, 0}
		for i := start + 1; i <= end; i++ {
			if thCount[car][clueOffset+i] == false {
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

func hasCars() bool {
	count := 0
	for i := 1; i < carMax; i++ {
		if thCount[i][0] == false {
			// no scans for car
			continue
		}
		count++
	}
	if count > 0 {
		return true
	}
	return false
}

func status() {
	count := 0
	for i := 1; i < carMax; i++ {
		if thCount[i][0] == false {
			// no scans for car
			continue
		}
		count++
	}
	log.Printf("%v cars counted\n", count)
}

func saveData() {
	writeState()
	if !hasCars() {
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
	count := 0
	for i := 1; i < carMax; i++ {
		if thCount[i][0] == false {
			// no scans for car
			//continue
		}
		emergencyStr := getCarEmergencies(i)
		clueStr := getCarClues(i)
		line := fmt.Sprintf("\t%v\t0\t%v\t%v\n", i, clueStr, emergencyStr)
		l, err := f.WriteString(line)
		if err != nil {
			fmt.Println(err)
			f.Close()
			return
		}
		_ = l
		count++
	}
	f.Close()
	log.Printf("%v cars saved\n", count)
}

func processCode(code string) {
	features := strings.Split(code, "-")
	if len(features) != 3 {
		log.Printf("Invalid number of code segments %v\n", len(features))
		return
	}
	car, _ := strconv.Atoi(features[0])
	thCount[car][0] = true
	cmd := features[1]
	switch cmd {
	case "QUIT":
		saveData()
		quit = true
	case "CLEAR":
		if car == 0 {
			saveData()
			thCount = new([carMax][totalCol]bool)
			thTimes = new([carMax]carTime)
			log.Println("All data cleared")
		} else {
			// clear car
			for i := 0; i < totalCol; i++ {
				thCount[car][i] = false
			}
			log.Printf("Car %v data cleared", car)
		}
	case "SAVE":
		saveData()
	case "STATUS":
		status()
	case "CL": // Clue
		clue := features[2][0] // get the first character
		clue = clue - 64
		thCount[car][clueOffset+clue] = true
	case "EM": // Emergency
		emergency, _ := strconv.Atoi(features[2])
		thCount[car][emergency] = true
	case "CA": // Car
		switch *thCommand {
		case "count":
			emergencies := 0
			clues := 0
			for i := 1; i < 53; i++ {
				if thCount[car][i] == true {
					if i <= clueNum {
						emergencies++
					} else {
						clues++
					}
				}
			}
			//log.Printf("car: %v emergencies: %v clues: %v\n", car, emergencies, clues)
			//log.Printf("car: %v emergency result %v clue result %v\n", car, getCarEmergencies(car), getCarClues(car))
			fmt.Println("--------------------")
			fmt.Printf("Car: %v scans: emergencies: %v \t clues: %v\n", car, emergencies, clues)
			fmt.Printf("Car: %v emergencies opened (%v): %v \n", car, clueNum-emergencies, getCarEmergencies(car))
			fmt.Printf("Car: %v clues visited (%v): %v\n", car, clueNum-clues, getCarClues(car))
		case "checkin":
			thTimes[car].checkIn = time.Now()
			log.Printf("Car %v check-in time: %v\n", car, thTimes[car].checkIn.Format("15:04:05"))
		case "checkout":
			thTimes[car].checkOut = time.Now()
			log.Printf("Car %v check-out time: %v\n", car, thTimes[car].checkOut.Format("15:04:05"))
		}
	}
}

func worker(s *serial.Port, codes chan string, workerId int) {
	errorCount := 0
	buf := make([]byte, 256)
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
			} else {
				log.Printf("Error: %v\n", err)
				errorCount++
			}
		} else {
			errorCount = 0
			code := string(buf[:n])
			code = strings.TrimSuffix(code, "\r")
			code = strings.TrimSuffix(code, "\n")

			//log.Printf("[%v]length: %v data: %q code: %v codeLen: %v\n", workerId, n, buf[:n], code, len(code))
			processCode(code)
			//data = append(data, buf[:n]...)
			//codes <- code
		}
	}

}

func init() {
	thCommand = flag.String("command", "count", "Current command")
}

func readState() {
	_, err := os.Stat(carStateFile)
	if os.IsNotExist(err) {
		// doesn't exist; initialize it
		//componentClasses = make([]componentClass, 0)
	} else {
		// read metadata from file
		byteValue, _ := ioutil.ReadFile(carStateFile)

		// we unmarshal our byteArray which contains our
		// jsonFile's content into 'users' which we defined above
		json.Unmarshal(byteValue, &thCount)
	}

	_, err = os.Stat(timeStateFile)
	if os.IsNotExist(err) {
		// doesn't exist; initialize it
		//componentClasses = make([]componentClass, 0)
	} else {
		// read metadata from file
		byteValue, _ := ioutil.ReadFile(timeStateFile)

		// we unmarshal our byteArray which contains our
		// jsonFile's content into 'users' which we defined above
		json.Unmarshal(byteValue, &thTimes)
	}

}

func writeState() {
	file, _ := json.MarshalIndent(thCount, "", " ")
	ioutil.WriteFile(carStateFile, file, 0644)

	file, _ = json.MarshalIndent(thTimes, "", " ")
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

func main() {
	/*
		thCount = new([carMax][53]bool)
		//thCount[1][clueOffset+1] = true
		//thCount[1][clueOffset+5] = true
		//thCount[1][clueOffset+6] = true
		//thCount[1][clueOffset+7] = true
		thCount[1][clueOffset+13] = true
		thCount[1][clueOffset+15] = true
		//thCount[1][clueOffset+20] = true

		result := getCarClues(1)
		log.Println(result)
		return
	*/

	thCount = new([carMax][totalCol]bool)
	thTimes = new([carMax]carTime)
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

	readState()

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
		go func(i int) {
			defer wg.Done()
			worker(s, nil, i)
		}(i)
	}

	wg.Wait()
}
