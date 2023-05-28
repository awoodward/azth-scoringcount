# GOOS=darwin go build -o thcount thcount.go

GOOS=darwin go1.16.15 build -o thcount thcount.go
zip thcount-mac.zip thcount templates/*