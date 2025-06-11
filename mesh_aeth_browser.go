// Save this file as mesh_aeth_browser.go
// To initialize Go module and fetch dependencies:
//   go mod init mesh_aeth_browser
//   go get github.com/hajimehoshi/ebiten/v2
//   go get github.com/hajimehoshi/ebiten/v2/ebitenutil

package main

import (
	"container/list"
	"fmt"
	"image/color"
	"math"
	"math/rand"
	"sync"
	"time"
	"net"
	"net/http"
	"strings"
	"bytes"
	"encoding/base64"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/google/uuid"
	"github.com/skip2/go-qrcode"
)

const (
	UDP_PORT      = "3000"
	SCREEN_WIDTH  = 1280
	SCREEN_HEIGHT = 720

	GRID_WIDTH    = 20
	GRID_HEIGHT   = 20
	TILE_WIDTH    = 64
	TILE_HEIGHT   = 32
)

var (
	peers    []string
	dnsPaths = make(map[string][]string)
	mutex    sync.Mutex

	// Referral UUIDs
	generatedReferralUUIDs = make(map[string]bool)
	referralMutex          = &sync.Mutex{}

	// City grid and player navigation
	grid             [GRID_HEIGHT][GRID_WIDTH]Cell
	playerX, playerY int
	// Predefine target for pathfinding (center of city)
	targetX = GRID_WIDTH/2
	targetY = GRID_HEIGHT/2
)

type Cell struct {
	isRoad       bool
	buildingTier int
}

type Game struct{}

func main() {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	initCity(rnd)
	initAIDiscoveryNodes()

	// Start background mesh services
	go startUDPServer()
	go maintainNatPaths()
	go startAIWebServer()

	ebiten.SetWindowSize(SCREEN_WIDTH, SCREEN_HEIGHT)
	ebiten.SetWindowTitle("AETH Mesh Browser - Pathfinding Visualization")

	if err := ebiten.RunGame(&Game{}); err != nil {
		panic(err)
	}
}

// initCity generates an isometric city with roads and buildings
type CityRand interface{}
func initCity(rnd *rand.Rand) {
	centerX, centerY := GRID_WIDTH/2, GRID_HEIGHT/2
	// Create central cross roads
	for y := 0; y < GRID_HEIGHT; y++ {
		grid[y][centerX].isRoad = true
	}
	for x := 0; x < GRID_WIDTH; x++ {
		grid[centerY][x].isRoad = true
	}
	// Add radial roads
	for dir := 0; dir < 4; dir++ {
		x, y := centerX, centerY
		for k := 0; k < GRID_WIDTH; k++ {
			switch dir {
			case 0:
				x++
			case 1:
				y++
			case 2:
				x--
			case 3:
				y--
			}
			if x < 0 || x >= GRID_WIDTH || y < 0 || y >= GRID_HEIGHT {
				break
			}
			grid[y][x].isRoad = true
		}
	}
	// Assign buildings by growth pattern
	for y := 0; y < GRID_HEIGHT; y++ {
		for x := 0; x < GRID_WIDTH; x++ {
			if !grid[y][x].isRoad {
				d := math.Hypot(float64(x-centerX), float64(y-centerY))
				grid[y][x].buildingTier = int(math.Max(1, 4-d/5))
			}
		}
	}
	// Place player at grid edge for visualization
	playerX, playerY = 0, centerY
}

func (g *Game) Update() error {
	// Navigation on road grid with arrow keys
	newX, newY := playerX, playerY
	if ebiten.IsKeyPressed(ebiten.KeyArrowUp) {
		newY--
	}
	if ebiten.IsKeyPressed(ebiten.KeyArrowDown) {
		newY++
	}
	if ebiten.IsKeyPressed(ebiten.KeyArrowLeft) {
		newX--
	}
	if ebiten.IsKeyPressed(ebiten.KeyArrowRight) {
		newX++
	}
	if newX >= 0 && newX < GRID_WIDTH && newY >= 0 && newY < GRID_HEIGHT && grid[newY][newX].isRoad {
		playerX, playerY = newX, newY
	}
	// Trigger mesh navigation
	if ebiten.IsKeyPressed(ebiten.KeyEnter) {
		fmt.Printf("[Navigate] Player at (%d,%d)\n", playerX, playerY)
	}

	// Trigger referral link generation info
	if ebiten.IsKeyPressed(ebiten.KeyG) {
		fmt.Println("[Game] To generate a referral link and QR code, open your web browser to: http://localhost:8080/generate-referral")
	}
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	screen.Fill(color.RGBA{20, 20, 40, 255})
	// Compute path each frame
	path := findPath(playerX, playerY, targetX, targetY)
	// Draw grid and buildings
	for y := 0; y < GRID_HEIGHT; y++ {
		for x := 0; x < GRID_WIDTH; x++ {
			sx := (float64(x-y) * TILE_WIDTH / 2) + SCREEN_WIDTH/2
			sy := (float64(x+y) * TILE_HEIGHT / 2) + 50
			col := color.RGBA{0, 0, 0, 0}
			if grid[y][x].isRoad {
				col = color.RGBA{100, 100, 100, 255}
			} else {
				tier := grid[y][x].buildingTier
				shade := uint8(50 + tier*40)
				height := TILE_HEIGHT * float64(tier)
				col = color.RGBA{shade, shade/2, 0, 255}
				ebitenutil.DrawRect(screen, sx, sy-height, TILE_WIDTH, height, col)
				continue
			}
			ebitenutil.DrawRect(screen, sx, sy, TILE_WIDTH, TILE_HEIGHT, col)
		}
	}
	// Highlight path
	for _, p := range path {
		x, y := p[0], p[1]
		sx := (float64(x-y) * TILE_WIDTH / 2) + SCREEN_WIDTH/2
		sy := (float64(x+y) * TILE_HEIGHT / 2) + 50
		ebitenutil.DrawRect(screen, sx, sy, TILE_WIDTH, TILE_HEIGHT, color.RGBA{50, 200, 50, 128})
	}
	// Draw player
	sx := (float64(playerX-playerY) * TILE_WIDTH / 2) + SCREEN_WIDTH/2
	sy := (float64(playerX+playerY) * TILE_HEIGHT / 2) + 50 - TILE_HEIGHT
	ebitenutil.DebugPrintAt(screen, "@", int(sx+TILE_WIDTH/4), int(sy))

	// Instructions
	ebitenutil.DebugPrintAt(screen, "Arrows to move, Enter to mesh (Path to center in green)", 10, SCREEN_HEIGHT-70)

	// Network Status Display
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("UDP Server: Running on port %s", UDP_PORT), 10, SCREEN_HEIGHT-55)
	ebitenutil.DebugPrintAt(screen, "AI Web Server: Running on port 8080", 10, SCREEN_HEIGHT-45) // Assuming port 8080 as per startAIWebServer
	ebitenutil.DebugPrintAt(screen, "NAT Maintenance: Active", 10, SCREEN_HEIGHT-35)
}

func (g *Game) Layout(w, h int) (int, int) {
	return SCREEN_WIDTH, SCREEN_HEIGHT
}

// findPath performs BFS to find shortest path on roads
func findPath(sx, sy, tx, ty int) [][2]int {
	dirs := [][2]int{{0, -1}, {0, 1}, {-1, 0}, {1, 0}}
	visited := make([][]bool, GRID_HEIGHT)
	for i := range visited {
		visited[i] = make([]bool, GRID_WIDTH)
	}
	prev := make([][][2]int, GRID_HEIGHT)
	for i := range prev {
		prev[i] = make([][2]int, GRID_WIDTH)
	}
	q := list.New()
	q.PushBack([2]int{sx, sy})
	visited[sy][sx] = true

	found := false
	for q.Len() > 0 && !found {
		elem := q.Remove(q.Front()).([2]int)
		x, y := elem[0], elem[1]
		for _, d := range dirs {
			nx, ny := x+d[0], y+d[1]
			if nx >= 0 && nx < GRID_WIDTH && ny >= 0 && ny < GRID_HEIGHT && grid[ny][nx].isRoad && !visited[ny][nx] {
				visited[ny][nx] = true
				prev[ny][nx] = [2]int{x, y}
				if nx == tx && ny == ty {
					found = true
					break
				}
				q.PushBack([2]int{nx, ny})
			}
		}
	}
	// Reconstruct path
	path := make([][2]int, 0)
	if visited[ty][tx] {
		for curX, curY := tx, ty; !(curX == sx && curY == sy); {
			path = append([][2]int{{curX, curY}}, path...)
			px, py := prev[curY][curX][0], prev[curY][curX][1]
			curX, curY = px, py
		}
	}
	return path
}

// Remaining mesh and AETH code unchanged
func initAIDiscoveryNodes() { /* ... */ }

func startUDPServer() {
	addr, err := net.ResolveUDPAddr("udp", ":"+UDP_PORT)
	if err != nil {
		fmt.Println("Error resolving UDP address:", err)
		return
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		fmt.Println("Error listening on UDP port:", err)
		return
	}
	defer conn.Close()

	fmt.Printf("UDP server listening on port %s\n", UDP_PORT)

	buffer := make([]byte, 1024) // Buffer to store incoming messages

	for {
		n, remoteAddr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			fmt.Println("Error reading from UDP:", err)
			continue // Continue listening despite error
		}

		message := string(buffer[:n])
		fmt.Printf("Received message from %s: %s\n", remoteAddr.String(), message)

		processAETHMessage(remoteAddr.String(), message)
	}
}

func maintainNatPaths() {
	for {
		fmt.Println("[NAT] Attempting to maintain NAT paths...")

		// TODO: Implement STUN client logic to discover public IP and port
		// TODO: Implement UPnP logic to attempt port forwarding
		// TODO: Manage and refresh mappings
		// For now, we just simulate the work with a sleep.

		time.Sleep(5 * time.Minute)
	}
}

func startAIWebServer() {
	// Handler for /status
	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "AI Web Server is running")
	})

	// Handler for /generate-referral
	http.HandleFunc("/generate-referral", func(w http.ResponseWriter, r *http.Request) {
		newUUID := uuid.New().String()

		referralMutex.Lock()
		generatedReferralUUIDs[newUUID] = true
		referralMutex.Unlock()

		// Assuming server runs on localhost:8080 for referral links
		// This port is hardcoded in ListenAndServe call below as well.
		serverPort := "8080"
		referralLink := fmt.Sprintf("http://localhost:%s/r/%s", serverPort, newUUID)

		// Generate QR Code
		var png []byte
		png, err := qrcode.Encode(referralLink, qrcode.Medium, 256) // Medium ECC, 256x256 pixels
		if err != nil {
			http.Error(w, "Failed to generate QR code", http.StatusInternalServerError)
			fmt.Printf("[AI Web Server] Error generating QR code: %v\n", err)
			return
		}
		qrCodeBase64 := base64.StdEncoding.EncodeToString(png)
		qrCodeDataURI := "data:image/png;base64," + qrCodeBase64

		// Serve HTML Page
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		htmlContent := fmt.Sprintf(`
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Your AETH Referral Link</title>
</head>
<body>
    <h1>Share your AETH Referral Link!</h1>
    <p>Your unique referral link is: <a href="%s">%s</a></p>
    <p>Scan the QR code:</p>
    <img src="%s" alt="Referral QR Code">
</body>
</html>
`, referralLink, referralLink, qrCodeDataURI)
		fmt.Fprint(w, htmlContent)
		fmt.Printf("[AI Web Server] Generated referral link: %s\n", referralLink)
	})

	// Handler for /r/:uuid (referral page)
	http.HandleFunc("/r/", func(w http.ResponseWriter, r *http.Request) {
		extractedUUID := strings.TrimPrefix(r.URL.Path, "/r/")
		if extractedUUID == "" || strings.Contains(extractedUUID, "/") {
			http.NotFound(w, r)
			fmt.Printf("[AI Web Server] Attempted access to invalid referral path: %s\n", r.URL.Path)
			return
		}

		referralMutex.Lock()
		isValid := generatedReferralUUIDs[extractedUUID]
		referralMutex.Unlock()

		if isValid {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			htmlContent := `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>AETH Mesh Browser Referral</title>
</head>
<body>
    <h1>Welcome! You've been referred to AETH Mesh Browser!</h1>
    <p><a href="#" onclick="alert('Download would start here!'); return false;">Download Game</a></p>
    <!-- TODO: Log this referral access for AETH rewards system -->
</body>
</html>
`
			fmt.Fprint(w, htmlContent)
			fmt.Printf("[AI Web Server] Served referral page for UUID: %s\n", extractedUUID)
		} else {
			http.NotFound(w, r)
			fmt.Printf("[AI Web Server] Invalid or unknown referral UUID attempted: %s\n", extractedUUID)
		}
	})

	port := ":8080"
	fmt.Printf("[AI Web Server] Starting on port %s...\n", port)
	err := http.ListenAndServe(port, nil)
	if err != nil {
		fmt.Printf("[AI Web Server] Error starting server: %s\n", err)
	}
}

func processAETHMessage(peer string, msg string) {
	fmt.Printf("[AETH] Received message from %s: %s\n", peer, msg)

	// TODO: Parse message format (e.g., JSON, custom binary)
	// TODO: Implement switch statement or other logic to handle different AETH message types
	// switch message.Type {
	// case "PEER_DISCOVERY":
	//     // handle peer discovery
	// case "DATA_CHUNK":
	//     // handle data chunk
	// default:
	//     fmt.Println("Unknown AETH message type")
	// }
}

func contains(slice []string, s string) bool  { return false }
func max(a, b int) int                         { return 0 }
