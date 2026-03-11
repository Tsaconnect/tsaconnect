function generateFileName() {
    // Create a new Date object
    let currentDate = new Date();

    // Get the current year, month, day, hours, minutes, and seconds
    let year = currentDate.getFullYear();
    let month = currentDate.getMonth() + 1; // Months are zero-based
    let day = currentDate.getDate();
    let hours = currentDate.getHours();
    let minutes = currentDate.getMinutes();
    let seconds = currentDate.getSeconds();

    // Add a leading zero to month, day, hours, minutes, and seconds if they are less than 10
    month = month < 10 ? '0' + month : month;
    day = day < 10 ? '0' + day : day;
    hours = hours < 10 ? '0' + hours : hours;
    minutes = minutes < 10 ? '0' + minutes : minutes;
    seconds = seconds < 10 ? '0' + seconds : seconds;

    // Format the date and time as a string
    let formattedDate = year + month + day;
    let formattedTime = hours + minutes + seconds;

    // Create the filename using the date and time
    let fileName = 'file_' + formattedDate + '_' + formattedTime;

    return fileName;
}

module.exports={generateFileName}