GOOS=windows GOARCH=amd64 go build -o thcount.exe thcount.go

zip thcount-win.zip thcount.exe templates/*