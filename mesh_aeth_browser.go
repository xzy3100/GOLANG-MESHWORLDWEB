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
	"io"
	"errors"
	// "io/ioutil" // Pre Go 1.16, using io.ReadAll instead

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

		serverPort := "8080" // Assuming server runs on this port

		localIP, localErr := getLocalIP()
		publicIP, publicErr := getPublicIP()

		var localReferralSection, publicReferralSection string

		// Local IP Section
		if localErr == nil {
			localReferralLink := fmt.Sprintf("http://%s:%s/r/%s", localIP, serverPort, newUUID)
			var pngLocal []byte
			pngLocal, errLocalQR := qrcode.Encode(localReferralLink, qrcode.Medium, 256)
			if errLocalQR == nil {
				localQRCodeDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngLocal)
				localReferralSection = fmt.Sprintf(`
                <div class="ip-section">
                    <h2>Share on your Local Network:</h2>
                    <p>Link: <a href="%s" target="_blank">%s</a></p>
                    <img src="%s" alt="Local Network Referral QR Code">
                </div>`, localReferralLink, localReferralLink, localQRCodeDataURI)
			} else {
				localReferralSection = fmt.Sprintf(`
                <div class="ip-section">
                    <h2>Share on your Local Network:</h2>
                    <p>Referral Link (for %s): Available at <a href="%s" target="_blank">%s</a></p>
                    <p class="error-text">QR Code generation failed: %s</p>
                </div>`, localIP, localReferralLink, localReferralLink, errLocalQR.Error())
			}
		} else {
			localReferralSection = fmt.Sprintf(`
            <div class="ip-section">
                <h2>Share on your Local Network:</h2>
                <p class="error-text">Local Network IP: Not available or error: %s</p>
            </div>`, localErr.Error())
		}
		localReferralSection += `<hr class="section-divider">`


		// Public IP Section
		if publicErr == nil {
			publicReferralLink := fmt.Sprintf("http://%s:%s/r/%s", publicIP, serverPort, newUUID)
			var pngPublic []byte
			pngPublic, errPublicQR := qrcode.Encode(publicReferralLink, qrcode.Medium, 256)
			if errPublicQR == nil {
				publicQRCodeDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngPublic)
				publicReferralSection = fmt.Sprintf(`
                <div class="ip-section">
                    <h2>Share over the Internet (Advanced):</h2>
                    <p class="warning-text">WARNING: This link will only work for others if you have configured PORT FORWARDING on your router for port %s to this computer (Local IP: %s). Otherwise, it will not be reachable from the internet.</p>
                    <p>Link: <a href="%s" target="_blank">%s</a></p>
                    <img src="%s" alt="Public Internet Referral QR Code">
                </div>`, serverPort, localIP, publicReferralLink, publicReferralLink, publicQRCodeDataURI)
			} else {
				publicReferralSection = fmt.Sprintf(`
                <div class="ip-section">
                    <h2>Share over the Internet (Advanced):</h2>
                    <p class="warning-text">WARNING: This link will only work for others if you have configured PORT FORWARDING on your router for port %s to this computer (Local IP: %s). Otherwise, it will not be reachable from the internet.</p>
                    <p>Referral Link (for %s): Available at <a href="%s" target="_blank">%s</a></p>
                    <p class="error-text">QR Code generation failed: %s</p>
                </div>`, serverPort, localIP, publicIP, publicReferralLink, publicReferralLink, errPublicQR.Error())
			}
		} else {
			publicReferralSection = fmt.Sprintf(`
            <div class="ip-section">
                <h2>Share over the Internet (Advanced):</h2>
                <p class="error-text">Public IP: Not available or error: %s</p>
            </div>`, publicErr.Error())
		}

		// Serve HTML Page
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		htmlContent := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
    <title>Your AETH Referral Links</title>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <style type="text/css">
        body {
            margin: 0; padding: 0; background-color: #0a0514;
            font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif;
            color: #e0e0e0; text-align: center;
        }
        .content-wrapper {
            background: linear-gradient(135deg, rgba(22,0,60,0.85) 0%, rgba(50,0,100,0.85) 25%, rgba(0,100,120,0.85) 75%, rgba(0,20,80,0.85) 100%),
                        url('data:image/svg+xml;utf8,<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><defs><filter id="noise"><feTurbulence type="fractalNoise" baseFrequency="0.65" numOctaves="3" stitchTiles="stitch"/></filter></defs><rect width="100%" height="100%" filter="url(%23noise)" opacity="0.05"/></svg>');
            background-blend-mode: screen;
            padding: 20px; border-radius: 15px;
            box-shadow: 0 0 25px rgba(0, 150, 255, 0.5), 0 0 10px rgba(255,255,255,0.2) inset;
            width: 90%; max-width: 800px; margin: 30px auto;
            text-align: left; border: 1px solid rgba(120, 120, 255, 0.3);
            animation: holographicShimmer 10s infinite linear; position: relative;
        }
        @keyframes holographicShimmer {
            0%% { background-position: 0%% 0%%; } 50%% { background-position: 100%% 100%%; } 100%% { background-position: 0%% 0%%; }
        }
        h1 { /* Overall page title */
            color: #f0f0ff;
            text-shadow: 0 0 5px #c0c0ff, 0 0 10px #c0c0ff, 0 0 15px #8080ff, 0 0 20px #8080ff, 1px 1px 1px #ff00ff, -1px -1px 1px #00ffff;
            animation: textGlitch 0.15s infinite alternate;
            margin-bottom: 30px; text-align: center;
        }
        .ip-section { padding: 15px; margin-bottom: 15px; }
        .ip-section h2 { /* Section titles */
             color: #d0d0ff; margin-top: 10px; margin-bottom: 15px;
             border-bottom: 1px solid rgba(120, 120, 255, 0.3);
             padding-bottom: 10px; text-align: center;
        }
        @keyframes textGlitch {
            0%% { text-shadow: 0 0 5px #c0c0ff, 0 0 10px #c0c0ff, 0 0 15px #8080ff, 0 0 20px #8080ff, 2px 1px 1px #ff00ff, -1px -2px 1px #00ffff; }
            100%% { text-shadow: 0 0 5px #c0c0ff, 0 0 10px #c0c0ff, 0 0 15px #8080ff, 0 0 20px #8080ff, -2px -1px 1px #ff00ff, 1px 2px 1px #00ffff; }
        }
        p { color: #c0c0e0; margin-bottom: 12px; line-height: 1.6; }
        .ip-section p { text-align: center; } /* Center text within sections by default */
        .ip-section p a { word-break: break-all; } /* Break long links */
        .ip-section img { display: block; margin: 15px auto; }
        a { color: #80ffff; text-decoration: none; font-weight: bold; }
        a:hover { text-decoration: underline; color: #ffffff; }
        img {
            border: 2px solid #8080ff;
            box-shadow: 0 0 15px #a0a0ff, 0 0 25px #8080ff, 0 0 5px rgba(255,255,255,0.5) inset;
            margin-top: 10px; border-radius: 10px;
            max-width: 256px; height: auto;
            background-color: rgba(255,255,255,0.05);
        }
        .warning-text {
            color: orange !important; font-weight: bold;
            background-color: rgba(255, 165, 0, 0.15); /* Darker background for warning */
            padding: 12px; border-radius: 8px;
            border: 1px solid orange; margin-bottom:15px;
            text-align: left; /* Align warning text left for readability */
        }
        .error-text {
            color: #ff8080 !important; /* Reddish for errors */
            font-weight: bold;
            background-color: rgba(255,0,0,0.1); padding:10px; border-radius:5px;
        }
        .section-divider {
            border: 0; height: 1px;
            background-image: linear-gradient(to right, rgba(120, 120, 255, 0.1), rgba(120, 120, 255, 0.5), rgba(120, 120, 255, 0.1));
            margin: 25px 0;
        }
    </style>
</head>
<body>
    <div class="content-wrapper">
        <h1>Your AETH Referral Links</h1>
        %s <!-- Local IP Section -->
        %s <!-- Public IP Section -->
    </div>
</body>
</html>
`, localReferralSection, publicReferralSection)
		fmt.Fprint(w, htmlContent)
		fmt.Printf("[AI Web Server] Generated referral links page for UUID: %s (Local IP: %s, Public IP attempted: %s)\n", newUUID, localIP, publicIP) // Adjusted log
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
<html>
<head>
    <title>AETH Mesh Browser Referral</title>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <style type="text/css">
        /* CSS for referral landing page */
        body {
            margin: 0;
            padding: 0;
            background-color: #0a0514; /* Dark violet/blue */
            font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif;
            color: #e0e0e0; /* Light text for dark background */
            text-align: center;
        }
        .content-wrapper {
            background: linear-gradient(135deg, rgba(0,60,80,0.85) 0%, rgba(0,100,120,0.85) 25%, rgba(22,0,60,0.85) 75%, rgba(50,0,100,0.85) 100%),
                        url('data:image/svg+xml;utf8,<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><defs><filter id="noise"><feTurbulence type="fractalNoise" baseFrequency="0.7" numOctaves="2" stitchTiles="stitch"/></filter></defs><rect width="100%" height="100%" filter="url(%23noise)" opacity="0.07"/></svg>');
            background-blend-mode: screen;
            padding: 40px;
            border-radius: 15px;
            box-shadow: 0 0 30px rgba(0, 200, 255, 0.6), 0 0 15px rgba(255,255,255,0.25) inset;
            width: 80%;
            max-width: 600px;
            margin: 40px auto;
            text-align: center;
            border: 1px solid rgba(0, 200, 255, 0.4);
            animation: holographicShimmer 12s infinite linear; /* Slightly different timing */
            position: relative;
        }
        @keyframes holographicShimmer {
            0% { background-position: 0% 0%; }
            50% { background-position: 100% 100%; }
            100% { background-position: 0% 0%; }
        }
        h1 {
            color: #f0f0ff;
            text-shadow:
                0 0 5px #c0c0ff, 0 0 10px #c0c0ff, 0 0 15px #8080ff, 0 0 20px #8080ff,
                1px 1px 1px #ff00ff, -1px -1px 1px #00ffff;
            animation: textGlitch 0.15s infinite alternate;
            margin-bottom: 25px;
        }
        @keyframes textGlitch {
            0% { text-shadow: 0 0 5px #c0c0ff, 0 0 10px #c0c0ff, 0 0 15px #8080ff, 0 0 20px #8080ff, 1px 2px 1px #ff00ff, -2px -1px 1px #00ffff; }
            100% { text-shadow: 0 0 5px #c0c0ff, 0 0 10px #c0c0ff, 0 0 15px #8080ff, 0 0 20px #8080ff, -1px -2px 1px #ff00ff, 2px 1px 1px #00ffff; }
        }
        p { color: #c0c0e0; margin-top: 20px; line-height: 1.7; }
        .download-button {
            background: linear-gradient(45deg, #00c6ff, #0072ff);
            color: white;
            padding: 15px 30px; /* Enhanced padding */
            text-decoration: none;
            border-radius: 10px; /* More rounded */
            font-size: 1.3em; /* Larger font */
            font-weight: bold;
            display: inline-block;
            margin-top: 30px;
            border: 1px solid #0072ff;
            box-shadow: 0 0 10px #00c6ff, 0 0 20px #0072ff, inset 0 0 8px rgba(255,255,255,0.4);
            transition: all 0.3s ease;
            animation: buttonGlow 1.5s infinite alternate;
        }
        .download-button:hover {
            background: linear-gradient(45deg, #0072ff, #0052cc);
            box-shadow: 0 0 20px #0072ff, 0 0 30px #0052cc, inset 0 0 10px rgba(255,255,255,0.6);
            transform: translateY(-2px); /* Slight lift on hover */
        }
        @keyframes buttonGlow {
            from { box-shadow: 0 0 10px #00c6ff, 0 0 20px #0072ff, inset 0 0 8px rgba(255,255,255,0.4); }
            to { box-shadow: 0 0 20px #0072ff, 0 0 30px #0052cc, inset 0 0 10px rgba(255,255,255,0.6); }
        }
    </style>
</head>
<body>
    <div class="content-wrapper">
        <h1>Welcome! You've been referred to AETH Mesh Browser!</h1>
        <p>Join the AETH community and start exploring the decentralized web.</p>
        <p><a href="#" class="download-button" onclick="alert('Download would start here!'); return false;">Download Game</a></p>
        <!-- TODO: Log this referral access for AETH rewards system -->
    </div>
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

// getLocalIP attempts to find a non-loopback local IP address.
func getLocalIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, address := range addrs {
		// Check the address type and if it is not a loopback
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil { // Ensure it's an IPv4 address
				return ipnet.IP.String(), nil
			}
		}
	}
	return "", errors.New("cannot find local IP address")
}

// getPublicIP attempts to discover the public IP address using an external service.
func getPublicIP() (string, error) {
	// It's good practice to use a client with a timeout.
	client := http.Client{
		Timeout: 5 * time.Second,
	}
	resp, err := client.Get("https://api.ipify.org?format=text") // Simple text response
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get public IP: %s (status code: %d)", resp.Status, resp.StatusCode)
	}

	ipBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(ipBytes), nil
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
