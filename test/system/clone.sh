#!/bin/bash

# Script to clone a private GitHub repository using GitHub App authentication
# This script generates a JWT token, exchanges it for an installation access token,
# and uses that token to clone the repository.

set -xe  # Exit on error

# Configuration - Set these environment variables before running:
# GITHUB_APP_ID: Your GitHub App ID
# GITHUB_APP_INSTALLATION_ID: The installation ID for your GitHub App
# GITHUB_APP_PRIVATE_KEY_PATH: Path to your GitHub App's private key file
# REPO_URL: The repository URL (e.g., https://github.com/owner/repo.git)
# CLONE_DIR: (Optional) Directory to clone into
#
GITHUB_APP_ID=2625348
GITHUB_APP_INSTALLATION_ID=103412574
GITHUB_APP_PRIVATE_KEY_PATH=/Users/luigi/syntasso/test-ssh/github-app-testing-git-writer-private.key
REPO_URL=https://github.com/syntasso/testing-git-writer-private.git


# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Function to print colored output
print_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

# Check for required dependencies
check_dependencies() {
    local missing_deps=()

    if ! command -v git &> /dev/null; then
        missing_deps+=("git")
    fi

    if ! command -v openssl &> /dev/null; then
        missing_deps+=("openssl")
    fi

    if ! command -v curl &> /dev/null; then
        missing_deps+=("curl")
    fi

    if ! command -v jq &> /dev/null; then
        missing_deps+=("jq")
    fi

    if [ ${#missing_deps[@]} -ne 0 ]; then
        print_error "Missing required dependencies: ${missing_deps[*]}"
        print_info "Please install them and try again."
        exit 1
    fi
}

# Validate required environment variables
validate_config() {
    local missing_vars=()

    if [ -z "$GITHUB_APP_ID" ]; then
        missing_vars+=("GITHUB_APP_ID")
    fi

    if [ -z "$GITHUB_APP_INSTALLATION_ID" ]; then
        missing_vars+=("GITHUB_APP_INSTALLATION_ID")
    fi

    if [ -z "$GITHUB_APP_PRIVATE_KEY_PATH" ]; then
        missing_vars+=("GITHUB_APP_PRIVATE_KEY_PATH")
    fi

    if [ -z "$REPO_URL" ]; then
        missing_vars+=("REPO_URL")
    fi

    if [ ${#missing_vars[@]} -ne 0 ]; then
        print_error "Missing required environment variables: ${missing_vars[*]}"
        print_info "Please set them and try again."
        exit 1
    fi

    if [ ! -f "$GITHUB_APP_PRIVATE_KEY_PATH" ]; then
        print_error "Private key file not found: $GITHUB_APP_PRIVATE_KEY_PATH"
        exit 1
    fi
}

# Generate JWT token for GitHub App authentication
generate_jwt() {
    local app_id=$1
    local private_key_path=$2

    # JWT header
    local header='{"alg":"RS256","typ":"JWT"}'
    local header_base64=$(echo -n "$header" | openssl base64 -e -A | tr '+/' '-_' | tr -d '=')

    # JWT payload
    local now=$(date +%s)
    local exp=$((now + 600))  # Token expires in 10 minutes
    local payload="{\"iat\":$now,\"exp\":$exp,\"iss\":\"$app_id\"}"
    local payload_base64=$(echo -n "$payload" | openssl base64 -e -A | tr '+/' '-_' | tr -d '=')

    # Create signature
    local signature_input="${header_base64}.${payload_base64}"
    local signature=$(echo -n "$signature_input" | \
        openssl dgst -sha256 -sign "$private_key_path" | \
        openssl base64 -e -A | \
        tr '+/' '-_' | \
        tr -d '=')

    # Combine to create JWT
    echo "${header_base64}.${payload_base64}.${signature}"
}

# Get installation access token using JWT
get_installation_token() {
    local jwt=$1
    local installation_id=$2

    local response=$(curl -s -X POST \
        -H "Accept: application/vnd.github+json" \
        -H "Authorization: Bearer $jwt" \
        -H "X-GitHub-Api-Version: 2022-11-28" \
        "https://api.github.com/app/installations/$installation_id/access_tokens")

    local token=$(echo "$response" | jq -r '.token')

    if [ "$token" = "null" ] || [ -z "$token" ]; then
        print_error "Failed to get installation access token"
        echo "Response: $response" >&2
        exit 1
    fi

    echo "$token"
}

# Clone repository using the access token
clone_repo() {
    local token=$1
    local repo_url=$2
    local clone_dir=$3

    # Convert HTTPS URL to authenticated URL
    local auth_url=$(echo "$repo_url" | sed "s|https://|https://x-access-token:${token}@|")

    print_info "Cloning repository..."

    if [ -n "$clone_dir" ]; then
        git clone "$auth_url" "$clone_dir"
    else
        git clone "$auth_url"
    fi
}

# Main execution
main() {
    print_info "Starting GitHub App authenticated clone process..."

    # Check dependencies
    print_info "Checking dependencies..."
    check_dependencies

    # Validate configuration
    print_info "Validating configuration..."
    validate_config

    # Generate JWT
    print_info "Generating JWT token..."
    JWT=$(generate_jwt "$GITHUB_APP_ID" "$GITHUB_APP_PRIVATE_KEY_PATH")

    # Get installation access token
    print_info "Getting installation access token..."
    ACCESS_TOKEN=$(get_installation_token "$JWT" "$GITHUB_APP_INSTALLATION_ID")

    # Clone repository
    clone_repo "$ACCESS_TOKEN" "$REPO_URL" "$CLONE_DIR"

    print_info "Repository cloned successfully!"
}

# Run main function
main
