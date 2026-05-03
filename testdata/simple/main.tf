terraform {
  required_providers {
    null = {
      source  = "hashicorp/null"
      version = "~> 3.2"
    }
  }
}

resource "null_resource" "demo" {
  triggers = {
    timestamp = timestamp()
  }

  provisioner "local-exec" {
    command = "echo Hello from Terrain"
  }
}

output "message" {
  value = "Run plan again — null_resource always shows a change."
}
