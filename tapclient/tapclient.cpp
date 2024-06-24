#include <iostream>
#include <sys/types.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>
#include <cstring>

#define SOCKET_PATH "/tmp/tap_sock/tape81e9.sock"

int receive_fd(int socket) {
    struct msghdr msg = {0};

    // Buffer for the ancillary data
    char buf[CMSG_SPACE(sizeof(int))];
    memset(buf, 0, sizeof(buf));

    // Buffer for the actual message
    char dummy_data[1];
    struct iovec io = { .iov_base = dummy_data, .iov_len = sizeof(dummy_data) };

    msg.msg_iov = &io;
    msg.msg_iovlen = 1;
    msg.msg_control = buf;
    msg.msg_controllen = sizeof(buf);

    ssize_t len = recvmsg(socket, &msg, 0);
    if (len < 0) {
        std::cerr << "Failed to receive message: " << strerror(errno) << std::endl;
        return -1;
    }

    if (msg.msg_controllen < sizeof(struct cmsghdr)) {
        std::cerr << "No control message received" << std::endl;
        return -1;
    }

    struct cmsghdr* cmsg = CMSG_FIRSTHDR(&msg);

    if (cmsg == NULL) {
        std::cerr << "No control message received" << std::endl;
        return -1;
    }

    if (cmsg->cmsg_len != CMSG_LEN(sizeof(int))) {
        std::cerr << "Invalid control message length" << std::endl;
        return -1;
    }

    if (cmsg->cmsg_level != SOL_SOCKET || cmsg->cmsg_type != SCM_RIGHTS) {
        std::cerr << "Invalid control message level/type" << std::endl;
        return -1;
    }

    int fd;
    memcpy(&fd, CMSG_DATA(cmsg), sizeof(int));
    return fd;
}

int main() {
    int sock = socket(AF_UNIX, SOCK_STREAM, 0);
    if (sock < 0) {
        std::cerr << "Failed to create socket: " << strerror(errno) << std::endl;
        return 1;
    }

    struct sockaddr_un addr;
    memset(&addr, 0, sizeof(addr));
    addr.sun_family = AF_UNIX;
    strncpy(addr.sun_path, SOCKET_PATH, sizeof(addr.sun_path) - 1);

    if (connect(sock, (struct sockaddr*)&addr, sizeof(addr)) < 0) {
        std::cerr << "Failed to connect to socket: " << strerror(errno) << std::endl;
        close(sock);
        return 1;
    }

    int tap_fd = receive_fd(sock);
    if (tap_fd < 0) {
        std::cerr << "Failed to receive file descriptor" << std::endl;
        close(sock);
        return 1;
    }

    std::cout << "Received TAP device file descriptor: " << tap_fd << std::endl;

    // You can now use tap_fd to read/write from/to the TAP device
    char buffer[1024];
    ssize_t n = read(tap_fd, buffer, sizeof(buffer));
    if (n > 0) {
        std::cout << "Read " << n << " bytes from TAP device" << std::endl;
    } else {
        std::cerr << "Failed to read from TAP device: " << strerror(errno) << std::endl;
    }

    close(tap_fd);
    close(sock);
    return 0;
}
