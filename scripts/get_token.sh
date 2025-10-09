#!/bin/bash

# Define colors
red='\033[0;31m'
nc='\033[0m'  # No color

# Exit on error
set -e

# Request Token with user/pass
generateTokenResponse=$(curl -s --insecure -u "$PIA_USER:$PIA_PASS" "https://www.privateinternetaccess.com/gtoken/generateToken")

if [ "$(echo "$generateTokenResponse" | jq -r '.token')" == "" ]; then
  echo -e "${red}Could not authenticate with the login credentials provided!${nc}"
  exit 1
fi

# Parse the JSON response with jq - set TOKEN equal to it
TOKEN=$(echo "$generateTokenResponse" | jq -r '.token')

# Save token to file for other script to use:
echo -e "${TOKEN}" > /tmp/pia_token

# Announce token success:
echo -e "Token grabbed"
